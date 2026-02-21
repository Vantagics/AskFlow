//go:build linux

package fontcheck

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

// knownCJKFontFamilies are font family names commonly associated with CJK support.
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
	"SimSun",
	"SimHei",
	"Microsoft YaHei",
	"FangSong",
	"KaiTi",
}

// distroPackages maps package manager commands to their CJK font package names.
// Each entry: [installCmd, checkCmd, ...packageNames]
type pkgManager struct {
	name       string
	checkCmd   []string // command to test if this package manager exists
	installCmd []string // base install command (packages appended)
	packages   []string // CJK font packages to install
}

var packageManagers = []pkgManager{
	{
		name:       "apt",
		checkCmd:   []string{"apt-get", "--version"},
		installCmd: []string{"apt-get", "install", "-y"},
		packages:   []string{"fonts-noto-cjk"},
	},
	{
		name:       "yum",
		checkCmd:   []string{"yum", "--version"},
		installCmd: []string{"yum", "install", "-y"},
		packages:   []string{"google-noto-sans-cjk-ttc-fonts"},
	},
	{
		name:       "dnf",
		checkCmd:   []string{"dnf", "--version"},
		installCmd: []string{"dnf", "install", "-y"},
		packages:   []string{"google-noto-sans-cjk-ttc-fonts"},
	},
	{
		name:       "pacman",
		checkCmd:   []string{"pacman", "--version"},
		installCmd: []string{"pacman", "-S", "--noconfirm"},
		packages:   []string{"noto-fonts-cjk"},
	},
	{
		name:       "zypper",
		checkCmd:   []string{"zypper", "--version"},
		installCmd: []string{"zypper", "install", "-y"},
		packages:   []string{"noto-sans-cjk-fonts-ttc"},
	},
	{
		name:       "apk",
		checkCmd:   []string{"apk", "--version"},
		installCmd: []string{"apk", "add"},
		packages:   []string{"font-noto-cjk"},
	},
}

// ensureCJKFontsLinux is the Linux-specific implementation.
func ensureCJKFontsLinux() {
	found, families := detectCJKFonts()
	logFontStatus(found, families)
	if found {
		return
	}

	// Fonts missing — try to help
	if !isRoot() {
		printManualInstallInstructions()
		return
	}

	// We are root, attempt automatic installation
	if err := autoInstallCJKFonts(); err != nil {
		log.Printf("字体检查: 自动安装中文字体失败: %v", err)
		printManualInstallInstructions()
		return
	}

	// Refresh font cache
	if fc, err := exec.LookPath("fc-cache"); err == nil {
		_ = exec.Command(fc, "-f").Run()
	}

	// Verify
	found, families = detectCJKFonts()
	logFontStatus(found, families)
	if !found {
		log.Println("字体检查: 安装后仍未检测到中文字体，请手动检查")
	}
}

// detectCJKFonts uses fc-list to check for CJK font families.
func detectCJKFonts() (bool, []string) {
	fcList, err := exec.LookPath("fc-list")
	if err != nil {
		// fc-list not available, try checking font directories directly
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

	// Extract matched family names
	var matched []string
	for _, kw := range knownCJKFontFamilies {
		if strings.Contains(output, kw) {
			matched = append(matched, kw)
		}
	}
	if len(matched) == 0 {
		// fc-list returned results but none matched known families — still counts
		matched = append(matched, "(other CJK fonts)")
	}
	return true, matched
}

// detectCJKFontsByPath checks common font directories for CJK font files.
func detectCJKFontsByPath() (bool, []string) {
	fontDirs := []string{
		"/usr/share/fonts",
		"/usr/local/share/fonts",
		"/usr/share/fonts/truetype",
		"/usr/share/fonts/opentype",
	}
	cjkKeywords := []string{"noto", "cjk", "wenquanyi", "wqy", "droid", "source-han", "sourcehansans"}

	for _, dir := range fontDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			lower := strings.ToLower(e.Name())
			for _, kw := range cjkKeywords {
				if strings.Contains(lower, kw) {
					return true, []string{e.Name()}
				}
			}
		}
	}
	return false, nil
}

// isRoot returns true if the current process is running as root (uid 0).
func isRoot() bool {
	return os.Getuid() == 0
}

// autoInstallCJKFonts detects the package manager and installs CJK fonts.
func autoInstallCJKFonts() error {
	for _, pm := range packageManagers {
		if _, err := exec.LookPath(pm.checkCmd[0]); err != nil {
			continue
		}

		log.Printf("字体检查: 检测到包管理器 %s，正在安装中文字体...", pm.name)

		args := append(pm.installCmd[1:], pm.packages...)
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

// printManualInstallInstructions prints instructions for non-root users.
func printManualInstallInstructions() {
	log.Println("========================================")
	log.Println("  缺少中文字体 — PPT渲染将显示方框")
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
