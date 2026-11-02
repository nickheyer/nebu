package probes

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// PCI vendor ids of the GPUs read from sysfs
const (
	vendorAMD   = "0x1002"
	vendorIntel = "0x8086"
)

// Where distributions keep the PCI id database
var pciIDFiles = []string{"/usr/share/hwdata/pci.ids", "/usr/share/misc/pci.ids"}

var cardName = regexp.MustCompile(`^card[0-9]+$`)

// AMD and Intel GPUs through the kernel's DRM sysfs tree, which every driver fills without vendor tools
type drmSysfs struct {
	// The sysfs mount, /sys outside tests
	root string
	// The PCI id database files tried in order
	ids []string
	// The procfs mount whose fdinfo files name each process's device memory, /proc outside tests
	proc string
}

func (drmSysfs) ID() string          { return "drm-sysfs" }
func (drmSysfs) Description() string { return "AMD and Intel GPUs through /sys/class/drm" }
func (drmSysfs) Runs(os, _ string) bool {
	return on([]string{"linux"}, nil, os, "")
}

func (p drmSysfs) Run(ctx context.Context) host.Result {
	root := p.root
	if root == "" {
		root = "/sys"
	}
	ids := p.ids
	if ids == nil {
		ids = pciIDFiles
	}
	procRoot := p.proc
	if procRoot == "" {
		procRoot = "/proc"
	}
	entries, err := os.ReadDir(filepath.Join(root, "class", "drm"))
	if err != nil {
		return skipped(err.Error())
	}
	var cards []string
	for _, e := range entries {
		if cardName.MatchString(e.Name()) {
			cards = append(cards, e.Name())
		}
	}
	sort.Slice(cards, func(i, j int) bool { return cardNumber(cards[i]) < cardNumber(cards[j]) })
	names := pciNames{files: ids}
	var devices []*v1.Device
	var pools []*v1.MemoryPool
	byBus := map[string]string{}
	for _, card := range cards {
		dev := filepath.Join(root, "class", "drm", card, "device")
		vendor := sysfsValue(dev, "vendor")
		if vendor != vendorAMD && vendor != vendorIntel {
			continue
		}
		d, pool, err := drmDevice(card, dev, vendor, names)
		if err != nil {
			return failed(fmt.Errorf("%s: %w", card, err))
		}
		devices = append(devices, d)
		if pool != nil {
			pools = append(pools, pool)
			byBus[strings.ToLower(d.GetFacts()["pci_bus_id"])] = d.GetId()
		}
	}
	res := found(devices, pools, nil, rows(len(devices)))
	res.Processes = drmProcesses(procRoot, byBus)
	return res
}

// Dedicated memory each process holds on the cards, read from its open DRM nodes' fdinfo, one count per client
func drmProcesses(procRoot string, byBus map[string]string) []*v1.ProcessUse {
	entries, err := os.ReadDir(procRoot)
	if err != nil || len(byBus) == 0 {
		return nil
	}
	var out []*v1.ProcessUse
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() {
			continue
		}
		dir := filepath.Join(procRoot, e.Name())
		fds, err := os.ReadDir(filepath.Join(dir, "fd"))
		if err != nil {
			continue
		}
		seen := map[string]bool{}
		bytesOn := map[string]uint64{}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(dir, "fd", fd.Name()))
			if err != nil || !strings.HasPrefix(target, "/dev/dri/") {
				continue
			}
			info, err := os.ReadFile(filepath.Join(dir, "fdinfo", fd.Name()))
			if err != nil {
				continue
			}
			bus, client, n, ok := drmFdinfo(string(info))
			if !ok || seen[bus+"/"+client] {
				continue
			}
			seen[bus+"/"+client] = true
			bytesOn[bus] += n
		}
		for bus, n := range bytesOn {
			if id, ok := byBus[bus]; ok && n > 0 {
				out = append(out, &v1.ProcessUse{DeviceId: id, Pid: int32(pid), Bytes: n})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].GetPid() < out[j].GetPid() || (out[i].GetPid() == out[j].GetPid() && out[i].GetDeviceId() < out[j].GetDeviceId())
	})
	return out
}

// The PCI address, client id, and dedicated memory one fdinfo reports, resident counts preferred over the legacy totals
func drmFdinfo(info string) (bus, client string, n uint64, ok bool) {
	var resident, memory uint64
	hasResident := false
	for _, line := range strings.Split(info, "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch {
		case key == "drm-pdev":
			bus = strings.ToLower(value)
		case key == "drm-client-id":
			client = value
		case strings.HasPrefix(key, "drm-resident-") && dedicatedRegion(strings.TrimPrefix(key, "drm-resident-")):
			hasResident = true
			resident += fdinfoBytes(value)
		case strings.HasPrefix(key, "drm-memory-") && dedicatedRegion(strings.TrimPrefix(key, "drm-memory-")):
			memory += fdinfoBytes(value)
		}
	}
	if bus == "" {
		return "", "", 0, false
	}
	if hasResident {
		return bus, client, resident, true
	}
	return bus, client, memory, true
}

// Whether an fdinfo region is the card's own memory, vram on amdgpu and xe, local on i915
func dedicatedRegion(region string) bool {
	return strings.HasPrefix(region, "vram") || strings.HasPrefix(region, "local")
}

// A count fdinfo writes in bytes, KiB, or MiB
func fdinfoBytes(value string) uint64 {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return 0
	}
	n, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	if len(fields) > 1 {
		switch fields[1] {
		case "KiB":
			n <<= 10
		case "MiB":
			n <<= 20
		}
	}
	return n
}

// One card as a device, with a pool when the driver reports dedicated memory
func drmDevice(card, dev, vendor string, names pciNames) (*v1.Device, *v1.MemoryPool, error) {
	deviceID := sysfsValue(dev, "device")
	bus := ""
	if target, err := os.Readlink(dev); err == nil {
		bus = filepath.Base(target)
	}
	driver := ""
	if target, err := os.Readlink(filepath.Join(dev, "driver")); err == nil {
		driver = filepath.Base(target)
	}
	facts := map[string]string{"index": strings.TrimPrefix(card, "card"), "pci_bus_id": bus, "driver": driver, "vendor_id": vendor, "device_id": deviceID}
	d := &v1.Device{Kind: v1.DeviceKind_DEVICE_KIND_GPU, Facts: facts}
	var total, used uint64
	var err error
	switch vendor {
	case vendorAMD:
		d.Vendor = "amd"
		unique := strings.ToLower(strings.TrimPrefix(sysfsValue(dev, "unique_id"), "0x"))
		if unique != "" {
			facts["unique_id"] = unique
		}
		d.Id = amdDeviceID(unique, bus, card)
		d.Name = sysfsValue(dev, "product_name")
		if total, err = sysfsBytes(dev, "mem_info_vram_total"); err != nil {
			return nil, nil, err
		}
		if used, err = sysfsBytes(dev, "mem_info_vram_used"); err != nil {
			return nil, nil, err
		}
	case vendorIntel:
		d.Vendor = "intel"
		d.Id = "intel-" + strings.ToLower(bus)
		if bus == "" {
			d.Id = "intel-" + card
		}
		if total, err = intelVRAM(dev); err != nil {
			return nil, nil, err
		}
	}
	if d.Name == "" {
		d.Name = names.lookup(vendor, deviceID, sysfsValue(dev, "subsystem_vendor"), sysfsValue(dev, "subsystem_device"))
	}
	if d.Name == "" {
		d.Name = fmt.Sprintf("%s %s", d.Vendor, strings.TrimPrefix(deviceID, "0x"))
	}
	d.MemoryTotalBytes = total
	if total > used {
		d.MemoryFreeBytes = total - used
	}
	if total == 0 {
		facts["memory"] = "shared"
		return d, nil, nil
	}
	return d, &v1.MemoryPool{Id: d.Id, Kind: v1.PoolKind_POOL_KIND_DEVICE, DeviceId: d.Id, TotalBytes: total, FreeBytes: d.MemoryFreeBytes}, nil
}

// The id an AMD GPU keeps across reboots, its unique id, else its PCI address, else the card number
func amdDeviceID(unique, bus, card string) string {
	switch {
	case unique != "" && strings.Trim(unique, "0") != "":
		return "amd-" + unique
	case bus != "":
		return "amd-" + strings.ToLower(bus)
	}
	return "amd-" + card
}

// Dedicated memory of an Intel GPU from the xe driver's tiles or the i915 driver's local memory, zero when shared
func intelVRAM(dev string) (uint64, error) {
	tiles, _ := filepath.Glob(filepath.Join(dev, "tile*", "physical_vram_size_bytes"))
	var total uint64
	for _, path := range tiles {
		n, err := sysfsBytes(filepath.Dir(path), filepath.Base(path))
		if err != nil {
			return 0, err
		}
		total += n
	}
	if total > 0 {
		return total, nil
	}
	return sysfsBytes(dev, "lmem_total_bytes")
}

// A trimmed sysfs attribute, empty when the driver does not expose it
func sysfsValue(dir, name string) string {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// A sysfs attribute holding a byte count, zero when absent
func sysfsBytes(dir, name string) (uint64, error) {
	text := sysfsValue(dir, name)
	if text == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a byte count", name, text)
	}
	return n, nil
}

func cardNumber(card string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(card, "card"))
	return n
}

// Device names from the PCI id database, read on first lookup
type pciNames struct {
	files []string
}

// The board's name when the database lists the subsystem, else the chip's, empty when neither is known
func (p pciNames) lookup(vendor, device, subVendor, subDevice string) string {
	vendor, device = strip(vendor), strip(device)
	subVendor, subDevice = strip(subVendor), strip(subDevice)
	for _, file := range p.files {
		f, err := os.Open(file)
		if err != nil {
			continue
		}
		name := scanPCIIDs(f, vendor, device, subVendor, subDevice)
		f.Close()
		return name
	}
	return ""
}

// Finds the device under its vendor, and the subsystem under the device when listed
func scanPCIIDs(f *os.File, vendor, device, subVendor, subDevice string) string {
	sc := bufio.NewScanner(f)
	inVendor, inDevice, chip := false, false, ""
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		switch {
		case !strings.HasPrefix(line, "\t"):
			if inVendor {
				return chip
			}
			inVendor = strings.HasPrefix(line, vendor+"  ")
		case !inVendor:
		case !strings.HasPrefix(line, "\t\t"):
			if inDevice {
				return chip
			}
			if rest, ok := strings.CutPrefix(line, "\t"+device+"  "); ok {
				inDevice, chip = true, strings.TrimSpace(rest)
			}
		case inDevice:
			if rest, ok := strings.CutPrefix(line, "\t\t"+subVendor+" "+subDevice+"  "); ok {
				return strings.TrimSpace(rest)
			}
		}
	}
	return chip
}

// A PCI id as the database writes it, lower case without the 0x prefix
func strip(id string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(id), "0x"))
}
