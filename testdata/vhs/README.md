# VHS Visual Regression Tests

This directory contains [VHS](https://github.com/charmbracelet/vhs) tape files for visual regression testing of the NTM dashboard.

## Prerequisites

Install VHS:
```bash
# macOS
brew install charmbracelet/tap/vhs

# Linux (via Go)
go install github.com/charmbracelet/vhs@latest

# Arch Linux
yay -S vhs
```

For screenshot comparison, optionally install ImageMagick:
```bash
# macOS
brew install imagemagick

# Ubuntu/Debian
sudo apt install imagemagick
```

## Running Tests

### Run all visual regression tests:
```bash
./scripts/visual-regression.sh
```

The script automatically gives each VHS run its own writable temp directory so Chrome state does not collide with other local browser sessions.

### Run a specific test:
```bash
./scripts/visual-regression.sh dashboard-basic
```

### Update golden images:
```bash
./scripts/visual-regression.sh --update
```

### Run via Go test:
```bash
go test -v -run Visual ./tests/e2e/...
```

## Tape Files

- **dashboard-basic.tape** - Basic dashboard rendering at startup
- **dashboard-resize.tape** - Tier transitions when terminal is resized
- **dashboard-navigation.tape** - Keyboard navigation between panels
- **dashboard-refresh.tape** - Ticker updates and manual refresh
- **dashboard-minimum.tape** - Rendering at minimum terminal sizes

## Directory Structure

```
testdata/
├── vhs/           # VHS tape files (test scripts)
├── golden/        # Golden screenshots (expected results)
└── screenshots/   # Current test screenshots (generated)
```

## Writing New Tests

1. Create a new `.tape` file in `testdata/vhs/`
2. Follow the VHS syntax: https://github.com/charmbracelet/vhs
3. Use `Screenshot testdata/screenshots/<name>.png` for screenshots
4. The main screenshot should match the tape name (e.g., `dashboard-basic.png`)
5. Run `./scripts/visual-regression.sh --update <tape-name>` to create golden images

## CI Integration

Tests automatically skip if VHS is not installed, so CI environments without VHS will not fail. To enable visual regression in CI, install VHS in the test environment.
