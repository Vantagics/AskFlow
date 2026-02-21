//go:build linux

package fontcheck

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// knownCJKFontFamilies are font family names commonly found in fc-list output.
var knownCJKFontFamilies = []string{
	"Noto Sans CJK",
	"Noto Serif CJK",
	"WenQuanYi",
	"文泉驿",
	"Source Han Sans",
	"Source Han Serif",
	"思源黑体",
	"思源宋体",
	"Droid Sans Fallback",
	"AR PL",
}

// pkgManager describes how to install CJK fonts via a specific package manager.
type pkgManager struct {
	name       string
	binary     string   // binary name to probe via LookPath
	preInstall []string // optional command to run before install (e.g. apt-get update)
	installCmd []string // base install command (packages are appended)
	packages   []string // CJK font package names
}

var packageManagers = []pkgManager{
	{
		name:       "apt",
		binary:     "apt-get",
		preInstall: []string{"apt-get", "update", "-qq"},
		installCmd: []string{"apt-get", "install", "-y"},
		packages:   []string{"fonts-noto-cjk"},
	},
	{
		name:       "dnf",
		binary:     "dnf",
		installCmd: []string{"dnf", "install", "-y"},
		packages:   []string{"google-noto-sans-cjk-ttc-fonts"},
	},
	{
		name:       "yum",
		binary:     "yum",
		installCmd: []string{"yum", "install", "-y"},
		packages:   []string{"google-noto-sans-cjk-ttc-fonts"},
	},
	{
		name:       "pacman",
		binary:     "pacman",
		installCmd: []string{"pacman", "-S", "--noconfirm"},
		packages:   []string{"noto-fonts-cjk"},
	},
	{
		name:       "zypper",
		binary:     "zypper",
		installCmd: []string{"zypper", "install", "-y"},
		packages:   []string{"noto-sans-cjk-fonts-ttc"},
	},
	{
		name:       "apk",
		binary:     "apk",
		installCmd: []string{"apk", "add"},
		packages:   []string{"font-noto-cjk"},
	},
}

// EnsureCJKFonts checks for CJK fonts on Linux. If missing, it auto-installs
// when running as root, or prints manual instructions otherwise.
func EnsureCJKFonts() {
	found, families := detectCJKFonts()
	logFontStatus(found, families)
	if found {
		return
	}

	if !isRoot() {
		printManualInstallInstructions()
		return
	}

	// Running as root — attempt automatic installation
	if err := autoInstallCJKFonts(); err != nil {
		log.Printf("字体检查: 自动安装中文字体失败: %v", err)
		printManualInstallInstructions()
		return
	}

	// Refresh font cache after installation
	refreshFontCache()

	// Verify installation result
	found, families = detectCJKFonts()
	logFontStatus(found, families)
	if !found {
		log.Println("字体检查: 安装后仍未检测到中文字体，请手动检查")
	}
}

// detectCJKFonts uses fc-list to check for CJK font families.
// Falls back to scanning font directories if fc-list is unavailable.
func detectCJKFonts() (bool, []string) {
	fcList, err := exec.LookPath("fc-list")
	if err != nil {
		return detectCJKFontsByPath()
	}

	out, err := exec.Command(fcList, ":lang=zh").Output()
	if err != nil {
		return detectCJKFontsByPath()
	}

	output := string(out)
	if strings.TrimSpace(output) == "" {
		return false, nil
	}

	var matched []string
	for _, kw := range knownCJKFontFamilies {
		if strings.Contains(output, kw) {
			matched = append(matched, kw)
		}
	}
	if len(matched) == 0 {
		// fc-list returned results but none matched known families — still counts
		matched = append(matched, "other CJK fonts")
	}
	return true, matched
}

// detectCJKFontsByPath recursively scans common font directories for CJK font files.
func detectCJKFontsByPath() (bool, []string) {
	fontDirs := []string{
		"/usr/share/fonts",
		"/usr/local/share/fonts",
	}
	cjkKeywords := []string{"noto", "cjk", "wenquanyi", "wqy", "droid", "source-han", "sourcehansans"}

	for _, root := range fontDirs {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		found := false
		var match string
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			lower := strings.ToLower(d.Name())
			// Only check actual font files
			if !strings.HasSuffix(lower, ".ttf") && !strings.HasSuffix(lower, ".ttc") &&
				!strings.HasSuffix(lower, ".otf") {
				return nil
			}
			for _, kw := range cjkKeywords {
				if strings.Contains(lower, kw) {
					found = true
					match = d.Name()
					return filepath.SkipAll
				}
			}
			return nil
		})
		if found {
			return true, []string{match}
		}
	}
	return false, nil
}

// isRoot returns true if the current process is running as root (uid 0).
func isRoot() bool {
	return os.Getuid() == 0
}

// autoInstallCJKFonts detects the system package manager and installs CJK fonts.
func autoInstallCJKFonts() error {
	for _, pm := range packageManagers {
		if _, err := exec.LookPath(pm.binary); err != nil {
			continue
		}

		log.Printf("字体检查: 检测到包管理器 %s，正在安装中文字体...", pm.name)

		// Run pre-install command if defined (e.g. apt-get update)
		if len(pm.preInstall) > 0 {
			pre := exec.Command(pm.preInstall[0], pm.preInstall[1:]...)
			pre.Stdout = os.Stdout
			pre.Stderr = os.Stderr
			if err := pre.Run(); err != nil {
				log.Printf("字体检查: %s pre-install 命令失败 (继续尝试安装): %v", pm.name, err)
			}
		}

		// Build install command — copy slice to avoid mutating the global table
		args := make([]string, 0, len(pm.installCmd)-1+len(pm.packages))
		args = append(args, pm.installCmd[1:]...)
		args = append(args, pm.packages...)

		cmd := exec.Command(pm.installCmd[0], args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s install failed: %w", pm.name, err)
		}

		log.Printf("字体检查: 中文字体安装完成 (via %s)", pm.name)
		return nil
	}

	return fmt.Errorf("未找到支持的包管理器 (apt/yum/dnf/pacman/zypper/apk)")
}

// refreshFontCache runs fc-cache -f if available.
func refreshFontCache() {
	if fc, err := exec.LookPath("fc-cache"); err == nil {
		cmd := exec.Command(fc, "-f")
		_ = cmd.Run()
	}
}

// logFontStatus logs the result of font detection.
func logFontStatus(found bool, families []string) {
	if found {
		log.Printf("字体检查: 已检测到中文字体 (%s)", strings.Join(families, ", "))
	} else {
		log.Println("字体检查: 未检测到中文字体，PPT渲染可能显示方框")
	}
}

// printManualInstallInstructions prints instructions for non-root users.
func printManualInstallInstructions() {
	log.Println("========================================")
	log.Println("  缺少中文字体 - PPT渲染将显示方框")
	log.Println("  请使用 root 权限安装中文字体:")
	log.Println("")
	log.Println("  Debian/Ubuntu:")
	log.Println("    sudo apt-get install -y fonts-noto-cjk")
	log.Println("")
	log.Println("  CentOS/RHEL/Fedora:")
	log.Println("    sudo yum install -y google-noto-sans-cjk-ttc-fonts")
	log.Println("")
	log.Println("  Arch Linux:")
	log.Println("    sudo pacman -S noto-fonts-cjk")
	log.Println("")
	log.Println("  Alpine:")
	log.Println("    apk add font-noto-cjk")
	log.Println("========================================")
}
