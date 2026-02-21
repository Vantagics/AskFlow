// Package fontcheck provides system font detection and installation for CJK fonts.
// On Linux servers, missing Chinese fonts cause PPT-to-image rendering to show
// rectangles instead of text. This package detects and resolves the issue at startup.
package fontcheck

import (
	"log"
	"runtime"
)

// EnsureCJKFonts checks if CJK (Chinese/Japanese/Korean) fonts are available
// on the system. On Linux, it attempts to install them automatically if running
// as root, or prints instructions for the user otherwise.
// On non-Linux platforms, this is a no-op (fonts are typically bundled with the OS).
func EnsureCJKFonts() {
	if runtime.GOOS != "linux" {
		return
	}
	ensureCJKFontsLinux()
}

// logFontStatus logs the result of font detection.
func logFontStatus(found bool, families []string) {
	if found {
		log.Printf("字体检查: 已检测到中文字体 (%v)", families)
	} else {
		log.Println("字体检查: 未检测到中文字体，PPT渲染可能显示方框")
	}
}
