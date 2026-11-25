// Command mkicon draws the placeholder LOTUS logo (a lotus flower) and writes
// it as a Windows icon and a PNG:
//
//	go run ./tools/mkicon -ico installer/lotus.ico -png installer/lotus.png
//
// It is a stand-in until a designed logo exists. Replace installer/lotus.ico
// (and run "go run ./tools/mkicon -h" no more).
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

func main() {
	icoPath := flag.String("ico", "installer/lotus.ico", "icon file to write")
	pngPath := flag.String("png", "installer/lotus.png", "PNG file to write (512 pixels)")
	sysoPath := flag.String("syso", "", "also write a Windows (amd64) object file with the icon as a resource, which the Go linker puts into lotus.exe")
	flag.Parse()

	sizes := []int{16, 24, 32, 48, 64, 128, 256}
	var images [][]byte
	for _, s := range sizes {
		var buf bytes.Buffer
		if err := png.Encode(&buf, render(s)); err != nil {
			fail(err)
		}
		images = append(images, buf.Bytes())
	}
	if err := os.WriteFile(*icoPath, ico(sizes, images), 0o644); err != nil {
		fail(err)
	}
	if *sysoPath != "" {
		if err := os.WriteFile(*sysoPath, syso(sizes, images), 0o644); err != nil {
			fail(err)
		}
		fmt.Println("wrote", *sysoPath)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, render(512)); err != nil {
		fail(err)
	}
	if err := os.WriteFile(*pngPath, buf.Bytes(), 0o644); err != nil {
		fail(err)
	}
	fmt.Println("wrote", *icoPath, "and", *pngPath)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// ico packs PNG images into an ICO file (PNG entries are valid since Vista).
func ico(sizes []int, images [][]byte) []byte {
	var b bytes.Buffer
	le := binary.LittleEndian
	_ = binary.Write(&b, le, [3]uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		w := byte(s)
		if s >= 256 {
			w = 0 // 0 means 256
		}
		b.Write([]byte{w, w, 0, 0})
		_ = binary.Write(&b, le, [2]uint16{1, 32}) // planes, bits per pixel
		_ = binary.Write(&b, le, uint32(len(images[i])))
		_ = binary.Write(&b, le, uint32(offset))
		offset += len(images[i])
	}
	for _, im := range images {
		b.Write(im)
	}
	return b.Bytes()
}

// syso builds a COFF object file (Windows, amd64) holding one .rsrc section
// with the icon images as RT_ICON resources and an RT_GROUP_ICON that lists
// them. Linking it into a Go program gives the .exe its icon. The resource
// tree is language-neutral and each data entry's address is relocated to the
// section (IMAGE_REL_AMD64_ADDR32NB), as the linker requires.
func syso(sizes []int, images [][]byte) []byte {
	le := binary.LittleEndian
	n := len(sizes)

	// Layout of the section, in order: root directory, the two name-level
	// directories, one language-level directory per resource, the data
	// entries, the data.
	const dirHdr, dirEnt, dataEnt = 16, 8, 16
	rootOff := 0
	l2IconOff := rootOff + dirHdr + 2*dirEnt
	l2GroupOff := l2IconOff + dirHdr + n*dirEnt
	l3Off := l2GroupOff + dirHdr + dirEnt // n icon dirs, then the group's
	l3Size := dirHdr + dirEnt
	entOff := l3Off + (n+1)*l3Size
	dataOff := entOff + (n+1)*dataEnt

	group := new(bytes.Buffer)
	_ = binary.Write(group, le, [3]uint16{0, 1, uint16(n)})
	for i, s := range sizes {
		w := byte(s)
		if s >= 256 {
			w = 0
		}
		group.Write([]byte{w, w, 0, 0})
		_ = binary.Write(group, le, [2]uint16{1, 32})
		_ = binary.Write(group, le, uint32(len(images[i])))
		_ = binary.Write(group, le, uint16(i+1)) // the RT_ICON id
	}
	blobs := append(append([][]byte(nil), images...), group.Bytes())
	offs := make([]int, len(blobs))
	pos := dataOff
	for i, b := range blobs {
		offs[i] = pos
		pos += (len(b) + 3) &^ 3
	}
	sec := make([]byte, pos)
	put32 := func(at int, v uint32) { le.PutUint32(sec[at:], v) }
	put16 := func(at int, v uint16) { le.PutUint16(sec[at:], v) }
	dir := func(at, ids int) { put16(at+14, uint16(ids)) } // NumberOfIdEntries
	const sub = 0x80000000

	// root: RT_ICON (3) and RT_GROUP_ICON (14), in id order
	dir(rootOff, 2)
	put32(rootOff+16, 3)
	put32(rootOff+20, sub|uint32(l2IconOff))
	put32(rootOff+24, 14)
	put32(rootOff+28, sub|uint32(l2GroupOff))
	// icons by id 1..n, then the group (id 1)
	dir(l2IconOff, n)
	for i := 0; i < n; i++ {
		put32(l2IconOff+16+i*8, uint32(i+1))
		put32(l2IconOff+16+i*8+4, sub|uint32(l3Off+i*l3Size))
	}
	dir(l2GroupOff, 1)
	put32(l2GroupOff+16, 1)
	put32(l2GroupOff+20, sub|uint32(l3Off+n*l3Size))
	// language level: one neutral entry each, pointing at a data entry
	var relocs []int // where a data entry's OffsetToData is
	for i := 0; i <= n; i++ {
		at := l3Off + i*l3Size
		dir(at, 1)
		put32(at+16, 0) // language: neutral
		put32(at+20, uint32(entOff+i*dataEnt))
		e := entOff + i*dataEnt
		put32(e, uint32(offs[i])) // becomes an address through the relocation
		put32(e+4, uint32(len(blobs[i])))
		relocs = append(relocs, e)
		copy(sec[offs[i]:], blobs[i])
	}

	// the COFF file: header, section header, data, relocations, symbols
	const fileHdr, secHdr, reloc, sym = 20, 40, 10, 18
	rawOff := fileHdr + secHdr
	relOff := rawOff + len(sec)
	symOff := relOff + len(relocs)*reloc
	var b bytes.Buffer
	_ = binary.Write(&b, le, uint16(0x8664)) // amd64
	_ = binary.Write(&b, le, uint16(1))      // one section
	_ = binary.Write(&b, le, uint32(0))      // time
	_ = binary.Write(&b, le, uint32(symOff))
	_ = binary.Write(&b, le, uint32(1)) // one symbol
	_ = binary.Write(&b, le, uint16(0)) // no optional header
	_ = binary.Write(&b, le, uint16(0))
	b.Write([]byte(".rsrc\x00\x00\x00"))
	_ = binary.Write(&b, le, uint32(0)) // virtual size
	_ = binary.Write(&b, le, uint32(0)) // virtual address
	_ = binary.Write(&b, le, uint32(len(sec)))
	_ = binary.Write(&b, le, uint32(rawOff))
	_ = binary.Write(&b, le, uint32(relOff))
	_ = binary.Write(&b, le, uint32(0)) // line numbers
	_ = binary.Write(&b, le, uint16(len(relocs)))
	_ = binary.Write(&b, le, uint16(0))
	_ = binary.Write(&b, le, uint32(0xC0000040)) // initialized data, read, write
	b.Write(sec)
	for _, at := range relocs {
		_ = binary.Write(&b, le, uint32(at))
		_ = binary.Write(&b, le, uint32(0)) // symbol 0: the section
		_ = binary.Write(&b, le, uint16(3)) // IMAGE_REL_AMD64_ADDR32NB
	}
	b.Write([]byte(".rsrc\x00\x00\x00"))
	_ = binary.Write(&b, le, uint32(0)) // value
	_ = binary.Write(&b, le, uint16(1)) // section 1
	_ = binary.Write(&b, le, uint16(0)) // type
	b.Write([]byte{3, 0})               // static, no aux symbols
	_ = binary.Write(&b, le, uint32(4)) // string table: just its size
	return b.Bytes()
}

// petal describes one petal: its angle from straight up (degrees), its length
// and width as fractions of the picture, and its colour.
type petal struct {
	angle, length, width float64
	c                    color.NRGBA
}

var (
	background = color.NRGBA{0x0f, 0x3d, 0x4a, 0xff}
	petals     = []petal{ // drawn in this order, back to front
		{-72, 0.34, 0.12, color.NRGBA{0xe8, 0x7a, 0xa8, 0xff}},
		{72, 0.34, 0.12, color.NRGBA{0xe8, 0x7a, 0xa8, 0xff}},
		{-40, 0.40, 0.14, color.NRGBA{0xf2, 0x9b, 0xc0, 0xff}},
		{40, 0.40, 0.14, color.NRGBA{0xf2, 0x9b, 0xc0, 0xff}},
		{0, 0.44, 0.15, color.NRGBA{0xff, 0xd6, 0xe6, 0xff}},
	}
	leaf = color.NRGBA{0x3f, 0xa7, 0x7a, 0xff}
)

// coverage returns the colour at a point given in picture fractions (0..1),
// or a zero alpha for transparent.
func sample(x, y float64) color.NRGBA {
	// rounded square background
	const r = 0.22
	dx := math.Max(math.Abs(x-0.5)-(0.5-r), 0)
	dy := math.Max(math.Abs(y-0.5)-(0.5-r), 0)
	if dx*dx+dy*dy > r*r {
		return color.NRGBA{}
	}
	out := background
	// the flower grows from this point
	bx, by := 0.5, 0.74
	// a leaf pad under the flower: a flat ellipse
	if ex, ey := (x-0.5)/0.34, (y-0.78)/0.07; ex*ex+ey*ey <= 1 {
		out = leaf
	}
	for _, p := range petals {
		a := p.angle * math.Pi / 180
		// into the petal's frame: u across, v along (from the base)
		px, py := x-bx, by-y
		u := px*math.Cos(a) - py*math.Sin(a)
		v := px*math.Sin(a) + py*math.Cos(a)
		if v < 0 || v > p.length {
			continue
		}
		t := v / p.length
		// width profile: pointed at both ends, widest a third of the way up
		half := p.width * math.Sin(math.Pi*math.Pow(t, 0.7))
		if math.Abs(u) <= half {
			shade := 1 - 0.18*t // slightly darker toward the tip
			out = color.NRGBA{
				uint8(float64(p.c.R) * shade),
				uint8(float64(p.c.G) * shade),
				uint8(float64(p.c.B) * shade), 0xff,
			}
		}
	}
	return out
}

// render draws the logo at size x size with 4x4 supersampling.
func render(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	const ss = 4
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					c := sample((float64(px)+(float64(sx)+0.5)/ss)/float64(size), (float64(py)+(float64(sy)+0.5)/ss)/float64(size))
					al := float64(c.A) / 255
					r += float64(c.R) * al
					g += float64(c.G) * al
					b += float64(c.B) * al
					a += al
				}
			}
			n := float64(ss * ss)
			if a == 0 {
				continue
			}
			img.SetNRGBA(px, py, color.NRGBA{uint8(r / a), uint8(g / a), uint8(b / a), uint8(a / n * 255)})
		}
	}
	return img
}
