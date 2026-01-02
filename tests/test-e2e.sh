#!/bin/bash
# End-to-end test script for oci storage
# Tests: Chart push/pull/delete, Image push/pull/delete, Proxy

set -e

# Configuration
PORTAL_URL="${PORTAL_URL:-http://localhost:3030}"
AUTH="${PORTAL_AUTH:-admin:admin123}"
TIMEOUT="${TIMEOUT:-120}"
# Configurable test image for proxy (default: traefik:latest)
TEST_IMAGE="${TEST_IMAGE:-traefik}"
TEST_TAG="${TEST_TAG:-latest}"
# Set to 1 to keep test data after tests (useful for UI verification)
KEEP_TEST_DATA="${KEEP_TEST_DATA:-0}"

# Test chart location
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART_DIR="${SCRIPT_DIR}/../src/testdata/charts"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Test counter
TESTS_PASSED=0
TESTS_FAILED=0

log_info() {
    echo -e "${YELLOW}[INFO]${NC} $1"
}

log_pass() {
    echo -e "${GREEN}[PASS]${NC} $1"
    TESTS_PASSED=$((TESTS_PASSED + 1))
}

log_fail() {
    echo -e "${RED}[FAIL]${NC} $1"
    TESTS_FAILED=$((TESTS_FAILED + 1))
}

log_section() {
    echo ""
    echo -e "${BLUE}=== $1 ===${NC}"
}

# Test HTTP endpoint
test_endpoint() {
    local description="$1"
    local method="$2"
    local endpoint="$3"
    local expected_code="$4"

    local url="${PORTAL_URL}${endpoint}"
    local response_code

    if [ "$method" = "HEAD" ]; then
        response_code=$(curl -s -o /dev/null -w "%{http_code}" -I -u "$AUTH" --max-time "$TIMEOUT" "$url" 2>/dev/null)
    elif [ "$method" = "DELETE" ]; then
        response_code=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE -u "$AUTH" --max-time "$TIMEOUT" "$url" 2>/dev/null)
    else
        response_code=$(curl -s -o /dev/null -w "%{http_code}" -u "$AUTH" --max-time "$TIMEOUT" "$url" 2>/dev/null)
    fi

    if [ -z "$response_code" ] || [ "$response_code" = "000" ]; then
        response_code="000"
    fi

    if [ "$response_code" = "$expected_code" ]; then
        log_pass "$description (HTTP $response_code)"
        return 0
    else
        log_fail "$description - Expected $expected_code, got $response_code"
        return 1
    fi
}

echo "========================================"
echo "  oci storage E2E Test Suite"
echo "========================================"
echo ""
log_info "Portal URL: $PORTAL_URL"
log_info "Test images: docker.io/${TEST_IMAGE}:${TEST_TAG}, docker.io/${TEST_IMAGE}:${TEST_TAG_2:-v3.2}"
log_info "Test charts: my-chart (0.1.0, 0.1.1), my-second-chart (0.1.0, 0.2.0)"
echo ""

# Check server availability
log_section "Server Health Check"
for i in {1..10}; do
    if curl -s -o /dev/null -w "" "${PORTAL_URL}/health" 2>/dev/null; then
        log_pass "Server is available"
        break
    fi
    if [ $i -eq 10 ]; then
        log_fail "Server not available at ${PORTAL_URL}"
        exit 1
    fi
    sleep 1
done

test_endpoint "Health endpoint" "GET" "/health" "200"
test_endpoint "OCI v2 API" "GET" "/v2/" "200"

# ============================================
# CLEANUP PREVIOUS TEST DATA
# ============================================
log_section "Cleanup Previous Test Data"

# Get list of all existing charts and delete them
log_info "Removing any existing charts..."
EXISTING_CHARTS=$(curl -s -u "$AUTH" "${PORTAL_URL}/charts" 2>/dev/null)
for chart_name in my-chart my-second-chart; do
    for version in 0.1.0 0.1.1 0.2.0; do
        curl -s -X DELETE -u "$AUTH" "${PORTAL_URL}/chart/${chart_name}/${version}" 2>/dev/null || true
    done
done

# Get list of all proxy images and delete them
log_info "Removing any cached proxy images..."
EXISTING_IMAGES=$(curl -s -u "$AUTH" "${PORTAL_URL}/images" 2>/dev/null)
# Extract all proxy image names and tags
PROXY_IMAGES=$(echo "$EXISTING_IMAGES" | grep -o '"name":"proxy/[^"]*"' | cut -d'"' -f4 | sort -u 2>/dev/null || true)
for img_name in $PROXY_IMAGES; do
    # Get all tags for this image
    TAGS=$(echo "$EXISTING_IMAGES" | grep -A5 "\"name\":\"${img_name}\"" | grep -o '"tag":"[^"]*"' | cut -d'"' -f4 2>/dev/null || true)
    for tag in $TAGS; do
        log_info "  Deleting ${img_name}:${tag}"
        curl -s -X DELETE -u "$AUTH" "${PORTAL_URL}/image/${img_name}/${tag}" 2>/dev/null || true
    done
done
log_pass "Previous test data cleaned"

# ============================================
# CHART TESTS (using HTTP API directly)
# ============================================
log_section "Chart Tests (Multiple Charts & Versions)"

# Helper function to push a chart
push_chart() {
    local chart_file="$1"
    local chart_name="$2"
    local chart_version="$3"

    if [ ! -f "${CHART_DIR}/${chart_file}" ]; then
        log_fail "Test chart not found: ${CHART_DIR}/${chart_file}"
        return 1
    fi

    PUSH_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST -u "$AUTH" \
        -F "chart=@${CHART_DIR}/${chart_file}" \
        "${PORTAL_URL}/chart" 2>/dev/null)
    if [ "$PUSH_CODE" = "200" ] || [ "$PUSH_CODE" = "201" ] || [ "$PUSH_CODE" = "303" ]; then
        log_pass "Chart push (${chart_name}:${chart_version})"
        return 0
    else
        log_fail "Chart push (${chart_name}:${chart_version}) - got HTTP $PUSH_CODE"
        return 1
    fi
}

# Helper function to delete a chart
delete_chart() {
    local chart_name="$1"
    local chart_version="$2"

    DELETE_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE -u "$AUTH" \
        "${PORTAL_URL}/chart/${chart_name}/${chart_version}" 2>/dev/null)
    if [ "$DELETE_CODE" = "200" ]; then
        log_pass "Chart delete (${chart_name}:${chart_version})"
        return 0
    else
        log_fail "Chart delete (${chart_name}:${chart_version}) - got HTTP $DELETE_CODE"
        return 1
    fi
}

# Push multiple charts with multiple versions
log_info "Pushing multiple charts..."
push_chart "my-chart-0.1.0.tgz" "my-chart" "0.1.0"
push_chart "my-chart-0.1.1.tgz" "my-chart" "0.1.1"
push_chart "my-second-chart-0.1.0.tgz" "my-second-chart" "0.1.0"
push_chart "my-second-chart-0.2.0.tgz" "my-second-chart" "0.2.0"

# List charts
test_endpoint "List charts" "GET" "/charts" "200"

# Check all charts appear in list
CHARTS_RESPONSE=$(curl -s -u "$AUTH" "${PORTAL_URL}/charts" 2>/dev/null)

if echo "$CHARTS_RESPONSE" | grep -q "my-chart"; then
    log_pass "my-chart appears in /charts list"
else
    log_fail "my-chart not found in /charts list"
fi

if echo "$CHARTS_RESPONSE" | grep -q "my-second-chart"; then
    log_pass "my-second-chart appears in /charts list"
else
    log_fail "my-second-chart not found in /charts list"
fi

# Check multiple versions exist
if echo "$CHARTS_RESPONSE" | grep -q "0.1.0" && echo "$CHARTS_RESPONSE" | grep -q "0.1.1"; then
    log_pass "Multiple versions of my-chart exist (0.1.0, 0.1.1)"
else
    log_fail "Multiple versions not found for my-chart"
fi

if echo "$CHARTS_RESPONSE" | grep -q "0.1.0" && echo "$CHARTS_RESPONSE" | grep -q "0.2.0"; then
    log_pass "Multiple versions of my-second-chart exist (0.1.0, 0.2.0)"
else
    log_fail "Multiple versions not found for my-second-chart"
fi

# Download different versions
log_info "Downloading multiple chart versions..."
PULL_DIR=$(mktemp -d)

DOWNLOAD_CODE=$(curl -s -o "${PULL_DIR}/my-chart-0.1.0.tgz" -w "%{http_code}" -u "$AUTH" \
    "${PORTAL_URL}/chart/my-chart/0.1.0" 2>/dev/null)
if [ "$DOWNLOAD_CODE" = "200" ] && [ -f "${PULL_DIR}/my-chart-0.1.0.tgz" ]; then
    log_pass "Chart download (my-chart:0.1.0)"
else
    log_fail "Chart download (my-chart:0.1.0) - got HTTP $DOWNLOAD_CODE"
fi

DOWNLOAD_CODE=$(curl -s -o "${PULL_DIR}/my-chart-0.1.1.tgz" -w "%{http_code}" -u "$AUTH" \
    "${PORTAL_URL}/chart/my-chart/0.1.1" 2>/dev/null)
if [ "$DOWNLOAD_CODE" = "200" ] && [ -f "${PULL_DIR}/my-chart-0.1.1.tgz" ]; then
    log_pass "Chart download (my-chart:0.1.1)"
else
    log_fail "Chart download (my-chart:0.1.1) - got HTTP $DOWNLOAD_CODE"
fi

DOWNLOAD_CODE=$(curl -s -o "${PULL_DIR}/my-second-chart-0.2.0.tgz" -w "%{http_code}" -u "$AUTH" \
    "${PORTAL_URL}/chart/my-second-chart/0.2.0" 2>/dev/null)
if [ "$DOWNLOAD_CODE" = "200" ] && [ -f "${PULL_DIR}/my-second-chart-0.2.0.tgz" ]; then
    log_pass "Chart download (my-second-chart:0.2.0)"
else
    log_fail "Chart download (my-second-chart:0.2.0) - got HTTP $DOWNLOAD_CODE"
fi

rm -rf "$PULL_DIR"

# Delete one version, verify other remains
log_info "Testing partial deletion..."
delete_chart "my-chart" "0.1.0"

CHARTS_AFTER=$(curl -s -u "$AUTH" "${PORTAL_URL}/charts" 2>/dev/null)
if echo "$CHARTS_AFTER" | grep -q "0.1.1"; then
    log_pass "my-chart:0.1.1 still exists after deleting 0.1.0"
else
    log_fail "my-chart:0.1.1 was incorrectly deleted"
fi

# Cleanup: delete remaining charts (unless KEEP_TEST_DATA is set)
if [ "${KEEP_TEST_DATA}" = "1" ]; then
    log_info "Keeping test charts (KEEP_TEST_DATA=1)"
    # Re-push the deleted chart for complete data
    push_chart "my-chart-0.1.0.tgz" "my-chart" "0.1.0"
else
    log_info "Cleaning up all test charts..."
    delete_chart "my-chart" "0.1.1"
    delete_chart "my-second-chart" "0.1.0"
    delete_chart "my-second-chart" "0.2.0"

    # Verify all charts are gone
    CHARTS_FINAL=$(curl -s -u "$AUTH" "${PORTAL_URL}/charts" 2>/dev/null)
    if echo "$CHARTS_FINAL" | grep -q "my-chart\|my-second-chart"; then
        log_fail "Some charts still exist after cleanup"
    else
        log_pass "All test charts removed"
    fi
fi

# ============================================
# PROXY TESTS
# ============================================
log_section "Proxy Tests (Multiple Tags from Docker Hub)"

# Second test tag (use a stable version tag)
TEST_TAG_2="${TEST_TAG_2:-v3.2}"

# Skip upstream tests if requested
if [ "${SKIP_UPSTREAM_TESTS:-0}" != "1" ]; then

    # --- First Tag Test ---
    log_info "Fetching ${TEST_IMAGE}:${TEST_TAG} from Docker Hub..."

    MANIFEST_RESPONSE=$(curl -s -u "$AUTH" "${PORTAL_URL}/v2/proxy/docker.io/${TEST_IMAGE}/manifests/${TEST_TAG}" 2>/dev/null)
    MANIFEST_CODE=$(curl -s -o /dev/null -w "%{http_code}" -u "$AUTH" "${PORTAL_URL}/v2/proxy/docker.io/${TEST_IMAGE}/manifests/${TEST_TAG}" 2>/dev/null)

    if [ "$MANIFEST_CODE" = "200" ]; then
        log_pass "GET manifest ${TEST_IMAGE}:${TEST_TAG} (HTTP 200)"
    else
        log_fail "GET manifest ${TEST_IMAGE}:${TEST_TAG} - got HTTP $MANIFEST_CODE"
    fi

    # Check if it's a multi-arch manifest (OCI index or Docker manifest list)
    if echo "$MANIFEST_RESPONSE" | grep -q '"manifests"'; then
        log_pass "Multi-arch manifest detected for ${TEST_TAG}"

        # Extract first child manifest digest
        CHILD_DIGEST=$(echo "$MANIFEST_RESPONSE" | grep -o '"digest":"sha256:[a-f0-9]*"' | head -1 | cut -d'"' -f4)

        if [ -n "$CHILD_DIGEST" ]; then
            log_info "Testing child manifest fetch: $CHILD_DIGEST"

            CHILD_CODE=$(curl -s -o /dev/null -w "%{http_code}" -u "$AUTH" \
                "${PORTAL_URL}/v2/proxy/docker.io/${TEST_IMAGE}/manifests/${CHILD_DIGEST}" 2>/dev/null)

            if [ "$CHILD_CODE" = "200" ]; then
                log_pass "Child manifest fetch by digest (HTTP 200)"
            else
                log_fail "Child manifest fetch - got HTTP $CHILD_CODE"
            fi
        else
            log_fail "Could not extract child digest from manifest"
        fi
    else
        log_info "Single-arch manifest for ${TEST_TAG}"
    fi

    # --- Second Tag Test ---
    log_info "Fetching ${TEST_IMAGE}:${TEST_TAG_2} from Docker Hub..."

    MANIFEST_RESPONSE_2=$(curl -s -u "$AUTH" "${PORTAL_URL}/v2/proxy/docker.io/${TEST_IMAGE}/manifests/${TEST_TAG_2}" 2>/dev/null)
    MANIFEST_CODE_2=$(curl -s -o /dev/null -w "%{http_code}" -u "$AUTH" "${PORTAL_URL}/v2/proxy/docker.io/${TEST_IMAGE}/manifests/${TEST_TAG_2}" 2>/dev/null)

    if [ "$MANIFEST_CODE_2" = "200" ]; then
        log_pass "GET manifest ${TEST_IMAGE}:${TEST_TAG_2} (HTTP 200)"
    else
        log_fail "GET manifest ${TEST_IMAGE}:${TEST_TAG_2} - got HTTP $MANIFEST_CODE_2"
    fi

    # --- Verify both tags appear in /images ---
    log_info "Verifying proxied images metadata..."
    IMAGE_RESPONSE=$(curl -s -u "$AUTH" "${PORTAL_URL}/images" 2>/dev/null)

    if echo "$IMAGE_RESPONSE" | grep -q "proxy/docker.io/${TEST_IMAGE}"; then
        log_pass "Proxied image in /images list"
    else
        log_fail "Proxied image not in /images list"
    fi

    # Count how many tags we have for this image
    TAG_COUNT=$(echo "$IMAGE_RESPONSE" | grep -o "\"tag\":\"[^\"]*\"" | wc -l | tr -d ' ')
    if [ "$TAG_COUNT" -ge 2 ]; then
        log_pass "Multiple tags cached (found $TAG_COUNT tags)"
    else
        log_fail "Expected at least 2 tags, found $TAG_COUNT"
    fi

    # Check size is non-zero
    SIZE=$(echo "$IMAGE_RESPONSE" | grep -o '"size":[0-9]*' | head -1 | cut -d: -f2)
    if [ -n "$SIZE" ] && [ "$SIZE" != "0" ]; then
        log_pass "Image size is non-zero: $SIZE bytes"
    else
        log_fail "Image size is zero or missing"
    fi

    # Test image details endpoint for both tags
    test_endpoint "Image details (${TEST_TAG})" "GET" "/image/proxy/docker.io/${TEST_IMAGE}/${TEST_TAG}/details" "200"
    test_endpoint "Image details (${TEST_TAG_2})" "GET" "/image/proxy/docker.io/${TEST_IMAGE}/${TEST_TAG_2}/details" "200"

    # Delete first tag, verify second remains
    log_info "Testing partial image deletion..."
    test_endpoint "Delete ${TEST_IMAGE}:${TEST_TAG}" "DELETE" "/image/proxy/docker.io/${TEST_IMAGE}/${TEST_TAG}" "200"

    IMAGES_PARTIAL=$(curl -s -u "$AUTH" "${PORTAL_URL}/images" 2>/dev/null)
    if echo "$IMAGES_PARTIAL" | grep -q "${TEST_TAG_2}"; then
        log_pass "${TEST_IMAGE}:${TEST_TAG_2} still exists after deleting ${TEST_TAG}"
    else
        log_fail "${TEST_IMAGE}:${TEST_TAG_2} was incorrectly deleted"
    fi

    # Cleanup: delete remaining image (unless KEEP_TEST_DATA is set)
    if [ "${KEEP_TEST_DATA}" = "1" ]; then
        log_info "Keeping proxied images (KEEP_TEST_DATA=1)"
        # Re-fetch the deleted tag so we have complete data
        curl -s -u "$AUTH" "${PORTAL_URL}/v2/proxy/docker.io/${TEST_IMAGE}/manifests/${TEST_TAG}" >/dev/null 2>&1
    else
        log_info "Cleaning up proxied images..."
        test_endpoint "Delete ${TEST_IMAGE}:${TEST_TAG_2}" "DELETE" "/image/proxy/docker.io/${TEST_IMAGE}/${TEST_TAG_2}" "200"

        # Verify all images are gone
        IMAGES_AFTER=$(curl -s -u "$AUTH" "${PORTAL_URL}/images" 2>/dev/null)
        if echo "$IMAGES_AFTER" | grep -q "proxy/docker.io/${TEST_IMAGE}"; then
            log_fail "Proxied image still exists after cleanup"
        else
            log_pass "All proxied images removed"
        fi
    fi
else
    log_info "Skipping upstream proxy tests (SKIP_UPSTREAM_TESTS=1)"
fi

# ============================================
# RESULTS
# ============================================
echo ""
echo "========================================"
echo "  Test Results"
echo "========================================"
echo -e "${GREEN}Passed: $TESTS_PASSED${NC}"
echo -e "${RED}Failed: $TESTS_FAILED${NC}"
echo ""

if [ $TESTS_FAILED -gt 0 ]; then
    echo -e "${RED}Some tests failed!${NC}"
    exit 1
else
    echo -e "${GREEN}All tests passed!${NC}"
    exit 0
fi
