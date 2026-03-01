package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type device struct {
	id          string
	description string
}

func listDevices() ([]device, error) {
	fmt.Println("Searching for scanner devices, this may take a minute.")
	out, err := exec.Command("scanimage", "-L").Output()
	if err != nil {
		return nil, fmt.Errorf("scanimage -L failed: %w", err)
	}

	var devs []device
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "device `") {
			continue
		}
		// device 'backend:...' is a Description
		rest := line[len("device '"):]
		id, after, ok := strings.Cut(rest, "'")
		if !ok {
			continue
		}
		desc := ""
		if _, d, found := strings.Cut(after, " is a "); found {
			desc = strings.TrimSpace(d)
		}
		devs = append(devs, device{id: id, description: desc})
	}
	return devs, nil
}

func pickDevice(r *bufio.Reader, devs []device) device {
	fmt.Println("Available scanners:")
	for i, d := range devs {
		fmt.Printf("  [%d] %s – %s\n", i+1, d.id, d.description)
	}
	for {
		fmt.Print("Select scanner number: ")
		line, _ := r.ReadString('\n')
		line = strings.TrimSpace(line)
		n, err := strconv.Atoi(line)
		if err == nil && n >= 1 && n <= len(devs) {
			return devs[n-1]
		}
		fmt.Printf("Please enter a number between 1 and %d.\n", len(devs))
	}
}

func scanADF(dev device, outDir, prefix string, resolution int, source string) ([]string, error) {
	pattern := filepath.Join(outDir, prefix+"_%04d.jpg")
	args := []string{
		"-d", dev.id,
		"--format=jpeg",
		"--mode=Color",
		fmt.Sprintf("--resolution=%d", resolution),
		fmt.Sprintf("--source=%s", source),
		fmt.Sprintf("--batch=%s", pattern),
		"--batch-start=1",
	}

	cmd := exec.Command("scanimage", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Non-zero exit is expected when the ADF runs out of pages.
	_ = cmd.Run()

	glob := filepath.Join(outDir, prefix+"_*.jpg")
	matches, err := filepath.Glob(glob)
	if err != nil {
		return nil, fmt.Errorf("glob %s: %w", glob, err)
	}
	sort.Strings(matches)
	return matches, nil
}

func askReverseBack(r *bufio.Reader) bool {
	fmt.Println()
	fmt.Println("How did you reload the pages for the back side?")
	fmt.Println("  [1] Flipped the whole stack (most common) – backs will be reversed")
	fmt.Println("  [2] Same order as fronts – no reversal needed")
	for {
		fmt.Print("Choice [1/2]: ")
		line, _ := r.ReadString('\n')
		line = strings.TrimSpace(line)
		switch line {
		case "1", "":
			return true
		case "2":
			return false
		}
		fmt.Println("Please enter 1 or 2.")
	}
}

func organise(fronts, backs []string, outDir string, reverseBack bool) error {
	if len(fronts) != len(backs) {
		return fmt.Errorf("front page count (%d) does not match back page count (%d)", len(fronts), len(backs))
	}

	if reverseBack {
		for i, j := 0, len(backs)-1; i < j; i, j = i+1, j-1 {
			backs[i], backs[j] = backs[j], backs[i]
		}
	}

	for i := range fronts {
		pageNum := i + 1
		frontDst := filepath.Join(outDir, fmt.Sprintf("page_%04d_front.jpg", pageNum))
		backDst := filepath.Join(outDir, fmt.Sprintf("page_%04d_back.jpg", pageNum))

		if err := os.Rename(fronts[i], frontDst); err != nil {
			return fmt.Errorf("rename front page %d: %w", pageNum, err)
		}
		if err := os.Rename(backs[i], backDst); err != nil {
			return fmt.Errorf("rename back page %d: %w", pageNum, err)
		}
	}
	return nil
}

func optimise(files []string, quality int) {
	if len(files) == 0 {
		return
	}

	if _, err := exec.LookPath("mogrify"); err != nil {
		fmt.Println("\nImageMagick not found. Install it to enable JPEG quality optimisation:")
		fmt.Println("  sudo apt install imagemagick")
		return
	}

	fmt.Printf("\nOptimising %d files at quality %d…\n", len(files), quality)
	args := append([]string{"-quality", strconv.Itoa(quality)}, files...)
	cmd := exec.Command("mogrify", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "mogrify warning: %v\n", err)
	}
}

func pause(r *bufio.Reader, msg string) {
	fmt.Print(msg)
	_, _ = r.ReadString('\n')
}

func main() {
	resolution := flag.Int("resolution", 200, "Scan resolution in DPI")
	quality := flag.Int("quality", 75, "JPEG quality 1–100 (requires ImageMagick)")
	outputBase := flag.String("output", "scans", "Base output directory")
	source := flag.String("source", "ADF", "SANE source name")
	deviceFlag := flag.String("device", "", "Scanner device ID (skips interactive selection)")
	flag.Parse()

	r := bufio.NewReader(os.Stdin)

	// 1. Select device.
	var dev device
	if *deviceFlag != "" {
		dev = device{id: *deviceFlag, description: "(user-specified)"}
	} else {
		devs, err := listDevices()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing devices: %v\n", err)
			os.Exit(1)
		}
		if len(devs) == 0 {
			fmt.Fprintln(os.Stderr, "No scanners found. Is sane-utils installed and the scanner connected?")
			os.Exit(1)
		}
		dev = pickDevice(r, devs)
	}

	// 2. Create timestamped output directory.
	ts := time.Now().Format("20060102_150405")
	outDir := filepath.Join(*outputBase, ts)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Cannot create output directory %s: %v\n", outDir, err)
		os.Exit(1)
	}
	fmt.Printf("Output directory: %s\n", outDir)

	// 3. Scan fronts.
	pause(r, "\nLoad pages FACE DOWN in the ADF, then press Enter to start scanning fronts…")
	fmt.Println("Scanning front sides…")
	fronts, err := scanADF(dev, outDir, "front", *resolution, *source)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Front scan error: %v\n", err)
		os.Exit(1)
	}
	if len(fronts) == 0 {
		fmt.Fprintln(os.Stderr, "No front pages scanned. Check scanner and ADF source name.")
		os.Exit(1)
	}
	fmt.Printf("Scanned %d front page(s).\n", len(fronts))

	// 4. Scan backs.
	pause(r, "\nFlip the stack and reload pages into the ADF, then press Enter to scan backs…")
	fmt.Println("Scanning back sides…")
	backs, err := scanADF(dev, outDir, "back", *resolution, *source)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Back scan error: %v\n", err)
		os.Exit(1)
	}
	if len(backs) == 0 {
		fmt.Fprintln(os.Stderr, "No back pages scanned.")
		os.Exit(1)
	}
	fmt.Printf("Scanned %d back page(s).\n", len(backs))

	// 5. Validate counts.
	if len(fronts) != len(backs) {
		fmt.Fprintf(os.Stderr, "Mismatch: %d front(s) vs %d back(s).\n", len(fronts), len(backs))
		os.Exit(1)
	}

	// 6. Ask about stacking order and organise.
	reverse := askReverseBack(r)
	if err := organise(fronts, backs, outDir, reverse); err != nil {
		fmt.Fprintf(os.Stderr, "Organisation error: %v\n", err)
		os.Exit(1)
	}

	// 7. Optimise with ImageMagick if available.
	allPages, _ := filepath.Glob(filepath.Join(outDir, "page_*.jpg"))
	sort.Strings(allPages)
	optimise(allPages, *quality)

	// 8. Summary.
	fmt.Printf("\nDone. %d page(s) saved to %s\n", len(fronts), outDir)
	for _, f := range allPages {
		fmt.Printf("  %s\n", f)
	}
}
