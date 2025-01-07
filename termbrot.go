// Package main defines the termbrot command line tool.
package main

import (
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"log"
	"math"
	"math/cmplx"
	"os"
	"sync"
	"time"

	"github.com/golang/freetype/truetype"
	"github.com/nsf/termbox-go"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/math/fixed"
	"golang.org/x/sys/unix"
)

// Constants
const (
	// X/Y aspect ratio
	aspect = 1.0

	// Fraction of the radius we pan on each keypress
	pan = 0.2

	// Factor we zoom in on each keypress
	zoom = 2
)

// Globals
var (
	showHelp     = true
	showInfo     = true
	center       complex128
	radius       float64
	depth        int
	textFace     font.Face
	plotDuration time.Duration
	decompose    = false
)

// reset to the start position
func reset() {
	center = complex(0, 0)
	radius = 2.0
	depth = 256
}

// Gradient colors
var gradient = []color.RGBA{
	{0, 0, 0, 255},       // Black
	{0, 0, 255, 255},     // Blue
	{255, 0, 0, 255},     // Red
	{255, 255, 0, 255},   // Yellow
	{255, 255, 255, 255}, // White
}

// smoothColor maps the Mandelbrot iteration depth to an RGB color
// using the gradient defined above and the escape value
// for extra smoothness.
func smoothColor(i int, z complex128, maxDepth int) color.RGBA {
	if i == maxDepth {
		// Inside the set (black)
		return color.RGBA{0, 0, 0, 255}
	}

	// Smooth iteration count
	smooth := float64(i) + 1.0 - math.Log(math.Log(cmplx.Abs(z)))/math.Log(2.0)

	// Map smooth iteration to gradient index
	t := smooth / float64(maxDepth) // Normalized to [0, 1]
	t = math.Min(math.Max(t, 0), 1) // Clamp to [0, 1]

	// Find two colors in the gradient
	idx := int(t * float64(len(gradient)-1))
	frac := t*float64(len(gradient)-1) - float64(idx)

	c1 := gradient[idx]
	c2 := gradient[int(math.Min(float64(idx+1), float64(len(gradient)-1)))]

	// Interpolate between c1 and c2
	r := uint8(float64(c1.R)*(1-frac) + float64(c2.R)*frac)
	g := uint8(float64(c1.G)*(1-frac) + float64(c2.G)*frac)
	b := uint8(float64(c1.B)*(1-frac) + float64(c2.B)*frac)

	if decompose && imag(z) < 0 {
		// r ^= 0x10
		// g ^= 0x10
		b ^= 0x10
	}

	return color.RGBA{r, g, b, 255}
}

// calculateMandlebrotRectangle plots a horizontal rectangle from the mandelbrot set
//
// The result is set in line as uint8 (r, g, b) tuples
func calculateMandlebrotRectangle(fx, fy, dx float64, width int, line []byte, wg *sync.WaitGroup) {
	defer wg.Done()
	p := 0
	for x := 0; x < width; x++ {
		z := complex(0, 0)
		c := complex(fx, fy)
		var i int
		for i = 0; i < depth; i++ {
			if cmplx.Abs(z) >= 2 {
				break
			}
			z = z*z + c
		}
		col := smoothColor(i, z, depth)
		line[p+0] = col.R
		line[p+1] = col.G
		line[p+2] = col.B
		p += 3
		fx += dx
	}
}

// writeRGBAImage send an image.RGBA image data in chunks to the terminal.
func writeRGBAImage(img *image.RGBA) {
	width := img.Rect.Dx()
	height := img.Rect.Dy()
	chunkSize := 4096
	data := base64.StdEncoding.EncodeToString(img.Pix)
	for len(data) > 0 {
		m := "1"
		end := chunkSize
		if len(data) <= chunkSize {
			end = len(data)
			m = "0"
		}
		chunk := data[:end]
		data = data[end:]

		fmt.Printf("\033_Gf=32,a=T,s=%d,v=%d,q=2,m=%s;%s\033\\", width, height, m, chunk)
	}
}

// writeRGB sends raw RGB image data in chunks.
func writeRGB(rawData []byte, width, height int) {
	chunkSize := 4096
	data := base64.StdEncoding.EncodeToString(rawData)
	for len(data) > 0 {
		m := "1"
		end := chunkSize
		if len(data) <= chunkSize {
			end = len(data)
			m = "0"
		}
		chunk := data[:end]
		data = data[end:]

		fmt.Printf("\033_Gf=24,a=T,s=%d,v=%d,q=2,m=%s;%s\033\\", width, height, m, chunk)
	}
}

// getTerminalSize retrieves the terminal size in rows, columns, and pixels
func getTerminalSize() (int, int, int, int, error) {
	ws, err := unix.IoctlGetWinsize(unix.Stdout, unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return int(ws.Row), int(ws.Col), int(ws.Xpixel), int(ws.Ypixel), nil
}

// getImageDimensions sizes up the output image
//
// This is 1 cell less on x and y to work around bug? in ghostty
func getImageDimensions() (imageWidth, imageHeight, rows, cols, cellWidth, cellHeight int) {
	rows, cols, terminalWidth, terminalHeight, err := getTerminalSize()
	if err != nil {
		fmt.Printf("Error retrieving terminal size: %v\n", err)
		os.Exit(1)
	}
	cellWidth, cellHeight = terminalWidth/cols, terminalHeight/rows
	cols -= 1 // reduce cols and rows to work around terminal differences
	rows -= 1 // between kitty and ghostty
	imageWidth, imageHeight = cols*cellWidth, rows*cellHeight

	return imageWidth, imageHeight, rows, cols, cellWidth, cellHeight
}

// Gets the size of the image in set co-ordinates
func getSetSize(width, height int) (dx, dy float64) {
	// Choose shortest direction for radius
	if float64(height) > float64(width)/aspect {
		dx = 2 * radius / float64(width)
		dy = 2 * radius / float64(width) * aspect
	} else {
		dx = 2 * radius / float64(height) / aspect
		dy = 2 * radius / float64(height)
	}
	return dx, dy
}

// writeMandlebrotSet sends raw RGB data in chunks of chunkHeightPixels high
func writeMandlebrotSet() {
	width, height, _, _, _, cellHeight := getImageDimensions()
	dx, dy := getSetSize(width, height)

	rowSize := 3 * width
	data := make([]byte, cellHeight*rowSize)
	var wg sync.WaitGroup
	fy := imag(center) + dy*float64(-height/2)
	for h := 0; h < height; h += cellHeight {
		chunkHeight := cellHeight
		if h+chunkHeight > height {
			chunkHeight = height - h
		}
		data := data[:chunkHeight*rowSize]
		for y := h; y < h+chunkHeight; y++ {
			fx := real(center) + dx*float64(-width/2)
			wg.Add(1)
			go calculateMandlebrotRectangle(fx, fy, dx, width, data[(y-h)*rowSize:(y-h+1)*rowSize], &wg)
			fy += dy

		}
		wg.Wait()
		writeRGB(data, width, chunkHeight)
		fmt.Printf("\n")
		if len(data) == 0 {
			break
		}
	}
}

// loadFont loads the font
func loadFont() (*truetype.Font, error) {
	return truetype.Parse(gobold.TTF)
}

// drawText draws text onto an RGBA image using the specified font face
func drawText(img *image.RGBA, x, y int, text string, col color.Color) {
	point := fixed.Point26_6{
		X: fixed.Int26_6(x * 64),
		Y: fixed.Int26_6(y * 64),
	}
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(col),
		Face: textFace,
		Dot:  point,
	}
	d.DrawString(text)
}

// helpOverlay returns an image with the help text to overlay on the main image
func helpOverlay() *image.RGBA {
	width, height := 600, 300
	h := 22
	sp := 10
	infoY := h * 10
	if !showHelp {
		height = 7 * h
		infoY = h
	}
	textImg := image.NewRGBA(image.Rectangle{Max: image.Point{width, height}})
	white := color.RGBA{255, 255, 255, 255}
	g80 := color.RGBA{255, 255, 255, 204}
	if showHelp {
		drawText(textImg, sp, h*1, "Terminal Mandlebrot by ncw", white)
		drawText(textImg, sp, h*2, "• ←↑↓→ to pan", g80)
		drawText(textImg, sp, h*3, "• +/- or left/right click to zoom", g80)
		drawText(textImg, sp, h*4, "• [/] to change depth", g80)
		drawText(textImg, sp, h*5, "• h/i toggle help/info", g80)
		drawText(textImg, sp, h*6, "• d toggle binary decompose", g80)
		drawText(textImg, sp, h*7, "• q/ESC/c-C to quit", g80)
		drawText(textImg, sp, h*8, "• r to reset", g80)
	}
	if showInfo {
		b80 := color.RGBA{128, 128, 255, 204}
		drawText(textImg, sp, infoY+h*0, fmt.Sprintf("• Center %g", center), b80)
		drawText(textImg, sp, infoY+h*1, fmt.Sprintf("• Radius %g", radius), b80)
		drawText(textImg, sp, infoY+h*2, fmt.Sprintf("• Depth %d", depth), b80)
		drawText(textImg, sp, infoY+h*3, fmt.Sprintf("• Time %v", plotDuration), b80)
	}
	return textImg
}

// draw the Mandelbrot set and any help/info required
func draw() {
	// Home the cursor - don't clear the screen
	fmt.Printf("\033[H")
	t0 := time.Now()
	writeMandlebrotSet()
	plotDuration = time.Since(t0)

	if showHelp || showInfo {
		// Home the cursor and print text overlay
		fmt.Printf("\033[H")
		img := helpOverlay()
		writeRGBAImage(img)
	}
}

func main() {
	// Load font
	ttfFont, err := loadFont()
	if err != nil {
		fmt.Printf("Error loading font: %v\n", err)
		os.Exit(1)
	}

	// Create a font face for drawing text
	textFace = truetype.NewFace(ttfFont, &truetype.Options{
		Size:    20,
		DPI:     72,
		Hinting: font.HintingFull,
	})

	// Init termbox which will control most things about the
	// terminal, but it doesn't support images yet so we'll do
	// that by hand.
	err = termbox.Init()
	if err != nil {
		log.Fatal(err)
	}
	defer termbox.Close()
	termbox.SetInputMode(termbox.InputEsc | termbox.InputMouse)

	reset()
	draw()
	for {
		redraw := false
		ev := termbox.PollEvent()

		switch ev.Type {
		case termbox.EventKey:
			redraw = true
			switch ev.Key + termbox.Key(ev.Ch) {
			case termbox.KeyEsc, termbox.KeyCtrlC, 'q':
				return
			case termbox.KeyArrowUp:
				center += complex(0.0, -radius*pan)
			case termbox.KeyArrowDown:
				center += complex(0.0, radius*pan)
			case termbox.KeyArrowLeft:
				center += complex(-radius*pan, 0.0)
			case termbox.KeyArrowRight:
				center += complex(radius*pan, 0.0)
			case termbox.KeyPgup, '=', '+':
				radius /= zoom
			case termbox.KeyPgdn, '-', '_':
				radius *= zoom
			case ']':
				depth *= 2
			case '[':
				depth /= 2
				if depth < 64 {
					depth = 64
				}
			case 'h':
				showHelp = !showHelp
			case 'i':
				showInfo = !showInfo
			case 'd':
				decompose = !decompose
			case 'r':
				reset()
			default:
				redraw = false
			}
		case termbox.EventMouse:
			redraw = true
			switch ev.Key {
			case termbox.MouseLeft, termbox.MouseRight:
				width, height, rows, cols, _, _ := getImageDimensions()
				dx, dy := getSetSize(width, height)
				newReal := real(center) + dx*float64(ev.MouseX-cols/2)/float64(cols)*float64(width)
				newImag := imag(center) + dy*float64(ev.MouseY-rows/2)/float64(rows)*float64(height)
				center = complex(newReal, newImag)
				if ev.Key == termbox.MouseLeft && ev.Mod&termbox.ModAlt == 0 {
					radius /= zoom
				} else {
					radius *= zoom
				}
			case termbox.MouseWheelDown:
				radius *= zoom
			case termbox.MouseWheelUp:
				radius /= zoom
			default:
				redraw = false
			}
		case termbox.EventResize:
			redraw = true
		}
		if redraw {
			draw()
		}
	}
}
