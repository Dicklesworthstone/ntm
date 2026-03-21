#!/bin/bash
# visual-regression.sh - Run VHS visual regression tests
#
# This script runs VHS tape files and compares screenshots against golden images.
# It requires VHS to be installed (https://github.com/charmbracelet/vhs)
#
# Usage:
#   ./scripts/visual-regression.sh [--update] [tape-name]
#
# Options:
#   --update    Update golden images instead of comparing
#   tape-name   Run only the specified tape (e.g., "dashboard-basic")
#
# Exit codes:
#   0 - All tests passed (or golden images updated)
#   1 - Tests failed (visual differences detected)
#   2 - VHS not installed or other setup error

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
TESTDATA_DIR="${PROJECT_ROOT}/testdata"
VHS_DIR="${TESTDATA_DIR}/vhs"
GOLDEN_DIR="${TESTDATA_DIR}/golden"
SCREENSHOTS_DIR="${TESTDATA_DIR}/screenshots"
VHS_TMP_ROOT="${NTM_VHS_TMP_ROOT:-/tmp/ntm-vhs-$$-$(date +%Y%m%d_%H%M%S)}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

UPDATE_MODE=false
SPECIFIC_TAPE=""

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --update)
            UPDATE_MODE=true
            shift
            ;;
        *)
            SPECIFIC_TAPE="$1"
            shift
            ;;
    esac
done

# Check for VHS
if ! command -v vhs &> /dev/null; then
    echo -e "${YELLOW}VHS is not installed. Skipping visual regression tests.${NC}"
    echo "Install VHS from: https://github.com/charmbracelet/vhs"
    exit 0  # Exit successfully to not fail CI when VHS is not available
fi

# Ensure directories exist
mkdir -p "${GOLDEN_DIR}" "${SCREENSHOTS_DIR}" "${VHS_TMP_ROOT}"

# Clean screenshots directory
rm -f "${SCREENSHOTS_DIR}"/*.png

# Find tape files to run
if [[ -n "${SPECIFIC_TAPE}" ]]; then
    TAPES=("${VHS_DIR}/${SPECIFIC_TAPE}.tape")
    if [[ ! -f "${TAPES[0]}" ]]; then
        echo -e "${RED}Tape file not found: ${TAPES[0]}${NC}"
        exit 2
    fi
else
    TAPES=("${VHS_DIR}"/*.tape)
fi

FAILED=0
PASSED=0
UPDATED=0

echo "Running VHS visual regression tests..."
echo ""

for tape in "${TAPES[@]}"; do
    tape_name=$(basename "${tape}" .tape)
    tape_tmp_dir="${VHS_TMP_ROOT}/${tape_name}"
    mkdir -p "${tape_tmp_dir}"
    echo -n "  ${tape_name}: "

    # Run VHS tape
    cd "${PROJECT_ROOT}"
    if ! TMPDIR="${tape_tmp_dir}" vhs "${tape}" > /dev/null 2>&1; then
        echo -e "${RED}FAILED (VHS error)${NC}"
        ((FAILED++))
        continue
    fi

    # Find the main output screenshot for this tape
    main_screenshot="${SCREENSHOTS_DIR}/${tape_name}.png"
    golden_screenshot="${GOLDEN_DIR}/${tape_name}.png"

    if [[ ! -f "${main_screenshot}" ]]; then
        echo -e "${RED}FAILED (no screenshot produced)${NC}"
        ((FAILED++))
        continue
    fi

    if [[ "${UPDATE_MODE}" == true ]]; then
        # Update golden images
        cp "${main_screenshot}" "${golden_screenshot}"
        # Also copy any intermediate screenshots
        for screenshot in "${SCREENSHOTS_DIR}/${tape_name}-"*.png; do
            if [[ -f "${screenshot}" ]]; then
                cp "${screenshot}" "${GOLDEN_DIR}/"
            fi
        done
        echo -e "${YELLOW}UPDATED${NC}"
        ((UPDATED++))
    else
        # Compare against golden
        if [[ ! -f "${golden_screenshot}" ]]; then
            echo -e "${YELLOW}SKIPPED (no golden image)${NC}"
            echo "    Run with --update to create golden images"
            continue
        fi

        # Use ImageMagick compare if available, otherwise simple hash comparison
        if command -v compare &> /dev/null; then
            diff_metric=$(compare -metric AE "${golden_screenshot}" "${main_screenshot}" /dev/null 2>&1 || true)
            if [[ "${diff_metric}" == "0" ]]; then
                echo -e "${GREEN}PASSED${NC}"
                ((PASSED++))
            else
                echo -e "${RED}FAILED (${diff_metric} pixels differ)${NC}"
                # Create diff image
                compare "${golden_screenshot}" "${main_screenshot}" "${SCREENSHOTS_DIR}/${tape_name}-diff.png" 2>/dev/null || true
                ((FAILED++))
            fi
        else
            # Fallback to hash comparison
            golden_hash=$(sha256sum "${golden_screenshot}" | cut -d' ' -f1)
            current_hash=$(sha256sum "${main_screenshot}" | cut -d' ' -f1)
            if [[ "${golden_hash}" == "${current_hash}" ]]; then
                echo -e "${GREEN}PASSED${NC}"
                ((PASSED++))
            else
                echo -e "${RED}FAILED (hash mismatch)${NC}"
                ((FAILED++))
            fi
        fi
    fi
done

echo ""

if [[ "${UPDATE_MODE}" == true ]]; then
    echo "Updated ${UPDATED} golden image(s)"
    exit 0
fi

echo "Results: ${PASSED} passed, ${FAILED} failed"

if [[ ${FAILED} -gt 0 ]]; then
    echo ""
    echo "Diff images saved to: ${SCREENSHOTS_DIR}/*-diff.png"
    echo "Run with --update to update golden images if changes are intentional."
    exit 1
fi

exit 0
