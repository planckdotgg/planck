// Package winres makes the Windows resources of planck-client.exe and
// planck-host.exe and checks a built exe for them.
//
// MyGo's Windows backend gives its window class icon resource 1
// (LoadIconW(instance, 1)), and go build links the .syso files of a main
// package. So each command keeps rsrc_windows_amd64.syso, made by Generate
// from assets/planck-windows.ico and internal/version (go run
// ./scripts/winres generate). The file also holds the version resource
// that Explorer's Details tab and SmartScreen show.
package winres

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"errors"
	"fmt"
	"image/png"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
)

const (
	rtIcon      = 3
	rtGroupIcon = 14
	rtVersion   = 16
	groupID     = 1
	versionID   = 1
)

// IconSizes are the sizes the shell asks for at 100 % to 200 % scale: 16,
// 20, 24 for the small icon, 32, 40, 48 for the big one and the taskbar,
// and 64 to 256 for Explorer. The .ico must have each of them.
var IconSizes = []int{16, 20, 24, 32, 40, 48, 64, 128, 256}

// Exe describes one executable for its version resource.
type Exe struct {
	// Name is the file name without .exe, such as planck-client.
	Name        string
	Description string
}

// SysoName is the file go build links into a windows/amd64 build.
const SysoName = "rsrc_windows_amd64.syso"

// Targets are the shipped commands, by the folder of their main package.
var Targets = []struct {
	Dir string
	Exe Exe
}{
	{"cmd/planck-client", Exe{Name: "planck-client", Description: "Planck"}},
	{"cmd/planck-host", Exe{Name: "planck-host", Description: "Planck Host"}},
}

// Info is what a version resource says about a file.
type Info struct {
	Version string
	Strings map[string]string
}

func (e Exe) info(version string) Info {
	return Info{Version: version, Strings: map[string]string{
		"CompanyName":      "Planck",
		"FileDescription":  e.Description,
		"FileVersion":      version,
		"InternalName":     e.Name,
		"LegalCopyright":   "Copyright © The Planck Authors. Apache License 2.0.",
		"OriginalFilename": e.Name + ".exe",
		"ProductName":      "Planck",
		"ProductVersion":   version,
	}}
}

// Generate returns the amd64 .syso file for the icon, the exe and the
// version.
func Generate(icoPath string, exe Exe, version string) ([]byte, error) {
	data, err := os.ReadFile(icoPath)
	if err != nil {
		return nil, err
	}
	images, sizes, err := iconImages(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", icoPath, err)
	}
	ver, err := versionInfo(exe.info(version))
	if err != nil {
		return nil, err
	}
	var group bytes.Buffer
	le := binary.LittleEndian
	_ = binary.Write(&group, le, [3]uint16{0, 1, uint16(len(images))})
	icons := map[uint16][]byte{}
	for i, img := range images {
		id := uint16(i + 1)
		icons[id] = img
		dim := byte(sizes[i])
		if sizes[i] >= 256 {
			dim = 0
		}
		group.Write([]byte{dim, dim, 0, 0})
		_ = binary.Write(&group, le, [2]uint16{1, 32})
		_ = binary.Write(&group, le, uint32(len(img)))
		_ = binary.Write(&group, le, id)
	}
	return coff(map[uint16]map[uint16][]byte{
		rtIcon:      icons,
		rtGroupIcon: {groupID: group.Bytes()},
		rtVersion:   {versionID: ver},
	}), nil
}

// iconImages returns the PNG entries of an .ico in ascending size, and
// fails unless every one of IconSizes is there.
func iconImages(b []byte) ([][]byte, []int, error) {
	if len(b) < 6 || binary.LittleEndian.Uint16(b[0:]) != 0 || binary.LittleEndian.Uint16(b[2:]) != 1 {
		return nil, nil, errors.New("not an .ico file")
	}
	n := int(binary.LittleEndian.Uint16(b[4:]))
	bySize := map[int][]byte{}
	for i := range n {
		e := 6 + 16*i
		if e+16 > len(b) {
			return nil, nil, errors.New("the directory is cut short")
		}
		size := int(b[e])
		if size == 0 {
			size = 256
		}
		length, offset := binary.LittleEndian.Uint32(b[e+8:]), binary.LittleEndian.Uint32(b[e+12:])
		if uint64(offset)+uint64(length) > uint64(len(b)) {
			return nil, nil, fmt.Errorf("the %d px image is cut short", size)
		}
		img := b[offset : offset+length]
		if !bytes.HasPrefix(img, []byte("\x89PNG")) {
			return nil, nil, fmt.Errorf("the %d px image is not a PNG", size)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(img))
		if err != nil || cfg.Width != size || cfg.Height != size {
			return nil, nil, fmt.Errorf("the %d px entry does not hold a %d px PNG", size, size)
		}
		bySize[size] = img
	}
	for _, s := range IconSizes {
		if bySize[s] == nil {
			return nil, nil, fmt.Errorf("the icon has no %d px image", s)
		}
	}
	sizes := make([]int, 0, len(bySize))
	for s := range bySize {
		sizes = append(sizes, s)
	}
	slices.Sort(sizes)
	images := make([][]byte, len(sizes))
	for i, s := range sizes {
		images[i] = bySize[s]
	}
	return images, sizes, nil
}

// versionInfo builds a VS_VERSIONINFO block: the fixed file info, one
// StringTable for US English in UTF-16 (040904B0), and the matching
// Translation.
func versionInfo(info Info) ([]byte, error) {
	nums, err := versionNumbers(info.Version)
	if err != nil {
		return nil, err
	}
	le := binary.LittleEndian
	ms, ls := uint32(nums[0])<<16|uint32(nums[1]), uint32(nums[2])<<16
	fixed := make([]byte, 52)
	for i, v := range []uint32{
		0xFEEF04BD, 0x00010000, // signature, structure version
		ms, ls, ms, ls, // file and product version
		0x3F, 0, // flags mask, flags
		0x00040004, 1, 0, // VOS_NT_WINDOWS32, VFT_APP, no subtype
		0, 0, // date
	} {
		le.PutUint32(fixed[4*i:], v)
	}
	keys := make([]string, 0, len(info.Strings))
	for k := range info.Strings {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var strs [][]byte
	for _, k := range keys {
		v := utf16z(info.Strings[k])
		strs = append(strs, vsNode(k, 1, v, uint16(len(v)/2), nil))
	}
	table := vsNode("040904B0", 1, nil, 0, strs)
	sfi := vsNode("StringFileInfo", 1, nil, 0, [][]byte{table})
	vfi := vsNode("VarFileInfo", 1, nil, 0, [][]byte{vsNode("Translation", 0, []byte{0x09, 0x04, 0xB0, 0x04}, 4, nil)})
	return vsNode("VS_VERSION_INFO", 0, fixed, 52, [][]byte{sfi, vfi}), nil
}

// versionNumbers is the major, minor and patch of a semantic version; a
// prerelease or build suffix is left out of the numeric fields.
func versionNumbers(v string) ([3]uint16, error) {
	var out [3]uint16
	core, _, _ := strings.Cut(v, "-")
	core, _, _ = strings.Cut(core, "+")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return out, fmt.Errorf("version %q: want X.Y.Z", v)
	}
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 16)
		if err != nil {
			return out, fmt.Errorf("version %q: %w", v, err)
		}
		out[i] = uint16(n)
	}
	return out, nil
}

func utf16z(s string) []byte {
	u := append(utf16.Encode([]rune(s)), 0)
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return b
}

func pad4(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

// vsNode is one version-resource node: length, value length, type, key,
// padding, value, then each child on a 4-byte boundary. valueLen counts
// bytes for binary values and WCHARs for strings.
func vsNode(key string, typ uint16, value []byte, valueLen uint16, children [][]byte) []byte {
	b := make([]byte, 6)
	binary.LittleEndian.PutUint16(b[2:], valueLen)
	binary.LittleEndian.PutUint16(b[4:], typ)
	b = append(b, utf16z(key)...)
	if len(value) > 0 {
		b = append(pad4(b), value...)
	}
	for _, ch := range children {
		b = append(pad4(b), ch...)
	}
	binary.LittleEndian.PutUint16(b[0:], uint16(len(b)))
	return b
}

// coff builds an amd64 COFF object with one .rsrc section holding res,
// language neutral. The section has three levels of directories (type, id,
// language), the data entries, then the data; data entries hold
// image-relative addresses, which the linker fills in from the
// relocations (IMAGE_REL_AMD64_ADDR32NB).
func coff(res map[uint16]map[uint16][]byte) []byte {
	const machine, relocation = 0x8664, 0x0003
	le := binary.LittleEndian
	types := sortedKeys(res)
	dirSize := func(n int) int { return 16 + 8*n }
	size := dirSize(len(types))
	count := 0
	for _, t := range types {
		size += dirSize(len(res[t])) + len(res[t])*dirSize(1)
		count += len(res[t])
	}
	entriesAt := size
	dataAt := entriesAt + 16*count

	raw := make([]byte, dataAt)
	var data bytes.Buffer
	var relocs []uint32
	le.PutUint16(raw[14:], uint16(len(types)))
	next := dirSize(len(types))
	entry := entriesAt
	for i, t := range types {
		le.PutUint32(raw[16+8*i:], uint32(t))
		le.PutUint32(raw[16+8*i+4:], uint32(next)|0x80000000)
		ids := sortedKeys(res[t])
		idDir := next
		le.PutUint16(raw[idDir+14:], uint16(len(ids)))
		next += dirSize(len(ids))
		for j, id := range ids {
			le.PutUint32(raw[idDir+16+8*j:], uint32(id))
			le.PutUint32(raw[idDir+16+8*j+4:], uint32(next)|0x80000000)
			le.PutUint16(raw[next+14:], 1)
			le.PutUint32(raw[next+16:], 0)
			le.PutUint32(raw[next+20:], uint32(entry))
			next += dirSize(1)
			for data.Len()%8 != 0 {
				data.WriteByte(0)
			}
			le.PutUint32(raw[entry:], uint32(dataAt+data.Len()))
			le.PutUint32(raw[entry+4:], uint32(len(res[t][id])))
			relocs = append(relocs, uint32(entry))
			entry += 16
			data.Write(res[t][id])
		}
	}
	raw = pad4(append(raw, data.Bytes()...))

	var out bytes.Buffer
	const headerSize, sectionHeaderSize = 20, 40
	rawAt := headerSize + sectionHeaderSize
	relocAt := rawAt + len(raw)
	symbolsAt := relocAt + 10*len(relocs)
	_ = binary.Write(&out, le, struct {
		Machine, Sections                   uint16
		Time, Symbols, SymbolCount          uint32
		OptionalHeaderSize, Characteristics uint16
	}{machine, 1, 0, uint32(symbolsAt), 1, 0, 0})
	_ = binary.Write(&out, le, struct {
		Name                                        [8]byte
		VirtualSize, VirtualAddress, RawSize, RawAt uint32
		RelocAt, LineNumbersAt                      uint32
		Relocs, LineNumbers                         uint16
		Characteristics                             uint32
	}{[8]byte{'.', 'r', 's', 'r', 'c'}, 0, 0, uint32(len(raw)), uint32(rawAt), uint32(relocAt), 0, uint16(len(relocs)), 0, 0x40000040})
	out.Write(raw)
	for _, r := range relocs {
		_ = binary.Write(&out, le, r)
		_ = binary.Write(&out, le, uint32(0))
		_ = binary.Write(&out, le, uint16(relocation))
	}
	out.Write([]byte{'.', 'r', 's', 'r', 'c', 0, 0, 0})
	_ = binary.Write(&out, le, uint32(0))
	_ = binary.Write(&out, le, uint16(1))
	_ = binary.Write(&out, le, uint16(0))
	out.Write([]byte{3, 0})
	_ = binary.Write(&out, le, uint32(4))
	return out.Bytes()
}

func sortedKeys[V any](m map[uint16]V) []uint16 {
	keys := make([]uint16, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Check reads the resources of a PE file. It returns the sizes of icon
// group 1 and the version resource, and fails when the group misses one of
// IconSizes or an image it lists, or when there is no version resource.
func Check(path string) ([]int, Info, error) {
	f, err := pe.Open(path)
	if err != nil {
		return nil, Info{}, err
	}
	defer f.Close()
	sec := f.Section(".rsrc")
	if sec == nil {
		return nil, Info{}, errors.New("no .rsrc section: the exe has no resources (was it built without its .syso file?)")
	}
	raw, err := sec.Data()
	if err != nil {
		return nil, Info{}, err
	}
	r := rsrc{raw: raw, base: sec.VirtualAddress}
	group, err := r.lookup(rtGroupIcon, groupID)
	if err != nil {
		return nil, Info{}, fmt.Errorf("RT_GROUP_ICON %d: %w", groupID, err)
	}
	if len(group) < 6 {
		return nil, Info{}, errors.New("the icon group is cut short")
	}
	n := int(binary.LittleEndian.Uint16(group[4:]))
	var sizes []int
	for i := range n {
		e := 6 + 14*i
		if e+14 > len(group) {
			return nil, Info{}, errors.New("the icon group is cut short")
		}
		s := int(group[e])
		if s == 0 {
			s = 256
		}
		id := binary.LittleEndian.Uint16(group[e+12:])
		img, err := r.lookup(rtIcon, id)
		if err != nil {
			return nil, Info{}, fmt.Errorf("RT_ICON %d (%d px): %w", id, s, err)
		}
		if uint32(len(img)) != binary.LittleEndian.Uint32(group[e+8:]) {
			return nil, Info{}, fmt.Errorf("RT_ICON %d has %d bytes, the group says %d", id, len(img), binary.LittleEndian.Uint32(group[e+8:]))
		}
		sizes = append(sizes, s)
	}
	for _, s := range IconSizes {
		if !slices.Contains(sizes, s) {
			return sizes, Info{}, fmt.Errorf("the icon has no %d px image", s)
		}
	}
	ver, err := r.lookup(rtVersion, versionID)
	if err != nil {
		return sizes, Info{}, fmt.Errorf("RT_VERSION %d: %w", versionID, err)
	}
	info, err := parseVersionInfo(ver)
	return sizes, info, err
}

// parseVersionInfo reads the fixed version and the strings of every
// StringTable of a VS_VERSIONINFO block.
func parseVersionInfo(b []byte) (Info, error) {
	root, err := readVSNode(b)
	if err != nil {
		return Info{}, err
	}
	if root.key != "VS_VERSION_INFO" || len(root.value) < 52 || binary.LittleEndian.Uint32(root.value) != 0xFEEF04BD {
		return Info{}, errors.New("not a VS_VERSIONINFO block")
	}
	ms, ls := binary.LittleEndian.Uint32(root.value[8:]), binary.LittleEndian.Uint32(root.value[12:])
	info := Info{
		Version: fmt.Sprintf("%d.%d.%d", ms>>16, ms&0xFFFF, ls>>16),
		Strings: map[string]string{},
	}
	for _, sfi := range root.children {
		if sfi.key != "StringFileInfo" {
			continue
		}
		for _, table := range sfi.children {
			for _, s := range table.children {
				info.Strings[s.key] = decodeUTF16z(s.value)
			}
		}
	}
	return info, nil
}

type vsParsed struct {
	key      string
	value    []byte
	children []vsParsed
}

func readVSNode(b []byte) (vsParsed, error) {
	if len(b) < 6 {
		return vsParsed{}, errors.New("version node cut short")
	}
	le := binary.LittleEndian
	length, valueLen, typ := int(le.Uint16(b)), int(le.Uint16(b[2:])), le.Uint16(b[4:])
	if length < 6 || length > len(b) {
		return vsParsed{}, errors.New("version node length out of range")
	}
	b = b[:length]
	i := 6
	start := i
	for i+1 < len(b) && (b[i] != 0 || b[i+1] != 0) {
		i += 2
	}
	if i+1 >= len(b) {
		return vsParsed{}, errors.New("version node key not terminated")
	}
	n := vsParsed{key: decodeUTF16z(b[start:i])}
	i = (i + 2 + 3) &^ 3
	if typ == 1 {
		valueLen *= 2
	}
	if valueLen > 0 {
		if i+valueLen > len(b) {
			return vsParsed{}, errors.New("version node value cut short")
		}
		n.value = b[i : i+valueLen]
		i += valueLen
	}
	for {
		i = (i + 3) &^ 3
		if i >= len(b) {
			return n, nil
		}
		ch, err := readVSNode(b[i:])
		if err != nil {
			return vsParsed{}, err
		}
		n.children = append(n.children, ch)
		i += int(le.Uint16(b[i:]))
	}
}

func decodeUTF16z(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		c := binary.LittleEndian.Uint16(b[i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u))
}

type rsrc struct {
	raw  []byte
	base uint32
}

func (r rsrc) u32(at int) (uint32, error) {
	if at < 0 || at+4 > len(r.raw) {
		return 0, errors.New("the resource section is cut short")
	}
	return binary.LittleEndian.Uint32(r.raw[at:]), nil
}

// child finds the entry with the id in the directory at dir, and returns
// its offset and whether it is a directory.
func (r rsrc) child(dir int, id uint32) (int, bool, error) {
	if dir+16 > len(r.raw) {
		return 0, false, errors.New("the resource section is cut short")
	}
	named := int(binary.LittleEndian.Uint16(r.raw[dir+12:]))
	ids := int(binary.LittleEndian.Uint16(r.raw[dir+14:]))
	for i := named; i < named+ids; i++ {
		e := dir + 16 + 8*i
		name, err := r.u32(e)
		if err != nil {
			return 0, false, err
		}
		if id != 0 && name != id {
			continue
		}
		off, err := r.u32(e + 4)
		if err != nil {
			return 0, false, err
		}
		return int(off &^ 0x80000000), off&0x80000000 != 0, nil
	}
	return 0, false, errors.New("not found")
}

// lookup returns the data of the first language of a resource.
func (r rsrc) lookup(typ, id uint16) ([]byte, error) {
	at, isDir, err := r.child(0, uint32(typ))
	if err != nil || !isDir {
		return nil, errors.New("not found")
	}
	if at, isDir, err = r.child(at, uint32(id)); err != nil || !isDir {
		return nil, errors.New("not found")
	}
	if at, isDir, err = r.child(at, 0); err != nil || isDir {
		return nil, errors.New("no language entry")
	}
	rva, err := r.u32(at)
	if err != nil {
		return nil, err
	}
	size, err := r.u32(at + 4)
	if err != nil {
		return nil, err
	}
	start := int(rva) - int(r.base)
	if start < 0 || start+int(size) > len(r.raw) {
		return nil, errors.New("the data is outside the resource section")
	}
	return r.raw[start : start+int(size)], nil
}
