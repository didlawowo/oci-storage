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

# Test chart location
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART_DIR="${SCRIPT_DIR}/../src/testdata/charts"
TEST_CHART="my-chart-0.1.0.tgz"

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
log_info "Test image: docker.io/${TEST_IMAGE}:${TEST_TAG}"
log_info "Test chart: $TEST_CHART"
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
# CHART TESTS (using HTTP API directly)
# ============================================
log_section "Chart Tests (Push/List/Pull/Delete)"

# Check if test chart exists
if [ ! -f "${CHART_DIR}/${TEST_CHART}" ]; then
    log_fail "Test chart not found: ${CHART_DIR}/${TEST_CHART}"
else
    # Push chart via HTTP POST (303 redirect = success)
    log_info "Pushing chart via HTTP..."
    PUSH_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST -u "$AUTH" \
        -F "chart=@${CHART_DIR}/${TEST_CHART}" \
        "${PORTAL_URL}/chart" 2>/dev/null)
    if [ "$PUSH_CODE" = "200" ] || [ "$PUSH_CODE" = "201" ] || [ "$PUSH_CODE" = "303" ]; then
        log_pass "Chart push (my-chart:0.1.0)"
    else
        log_fail "Chart push - got HTTP $PUSH_CODE"
    fi

    # List charts
    test_endpoint "List charts" "GET" "/charts" "200"

    # Check chart appears in list
    CHARTS_RESPONSE=$(curl -s -u "$AUTH" "${PORTAL_URL}/charts" 2>/dev/null)
    if echo "$CHARTS_RESPONSE" | grep -q "my-chart"; then
        log_pass "Chart appears in /charts list"
    else
        log_fail "Chart not found in /charts list"
    fi

    # Download chart via HTTP (route: /chart/:name/:version)
    log_info "Downloading chart via HTTP..."
    PULL_DIR=$(mktemp -d)
    DOWNLOAD_CODE=$(curl -s -o "${PULL_DIR}/my-chart-0.1.0.tgz" -w "%{http_code}" -u "$AUTH" \
        "${PORTAL_URL}/chart/my-chart/0.1.0" 2>/dev/null)
    if [ "$DOWNLOAD_CODE" = "200" ] && [ -f "${PULL_DIR}/my-chart-0.1.0.tgz" ]; then
        log_pass "Chart download (my-chart:0.1.0)"
    else
        log_fail "Chart download - got HTTP $DOWNLOAD_CODE"
    fi
    rm -rf "$PULL_DIR"

    # Delete chart (route: /chart/:name/:version)
    log_info "Deleting chart..."
    DELETE_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE -u "$AUTH" \
        "${PORTAL_URL}/chart/my-chart/0.1.0" 2>/dev/null)
    if [ "$DELETE_CODE" = "200" ]; then
        log_pass "Chart delete (my-chart:0.1.0)"
    else
        log_fail "Chart delete - got HTTP $DELETE_CODE"
    fi

    # Verify chart is gone
    CHARTS_AFTER=$(curl -s -u "$AUTH" "${PORTAL_URL}/charts" 2>/dev/null)
    if echo "$CHARTS_AFTER" | grep -q "my-chart"; then
        log_fail "Chart still exists after delete"
    else
        log_pass "Chart removed from list"
    fi
fi

# ============================================
# PROXY TESTS
# ============================================
log_section "Proxy Tests (Docker Hub → ${TEST_IMAGE}:${TEST_TAG})"

# Clean any cached proxy images first
log_info "Cleaning proxy cache for test image..."
curl -s -X DELETE -u "$AUTH" "${PORTAL_URL}/image/proxy/docker.io/${TEST_IMAGE}/${TEST_TAG}" 2>/dev/null || true

# Skip upstream tests if requested
if [ "${SKIP_UPSTREAM_TESTS:-0}" != "1" ]; then
    log_info "Fetching manifest from Docker Hub (may take a while)..."
    test_endpoint "GET manifest via proxy" "GET" "/v2/proxy/docker.io/${TEST_IMAGE}/manifests/${TEST_TAG}" "200"

    # Verify image appears in /images
    log_info "Verifying proxied image metadata..."
    IMAGE_RESPONSE=$(curl -s -u "$AUTH" "${PORTAL_URL}/images" 2>/dev/null)

    if echo "$IMAGE_RESPONSE" | grep -q "proxy/docker.io/${TEST_IMAGE}"; then
        log_pass "Proxied image in /images list"
    else
        log_fail "Proxied image not in /images list"
    fi

    # Check size is non-zero
    SIZE=$(echo "$IMAGE_RESPONSE" | grep -o '"size":[0-9]*' | head -1 | cut -d: -f2)
    if [ -n "$SIZE" ] && [ "$SIZE" != "0" ]; then
        log_pass "Image size is non-zero: $SIZE bytes"
    else
        log_fail "Image size is zero or missing"
    fi

    # Test image details endpoint
    test_endpoint "Image details page" "GET" "/image/proxy/docker.io/${TEST_IMAGE}/${TEST_TAG}/details" "200"

    # Delete proxied image
    log_info "Deleting proxied image..."
    test_endpoint "Delete proxied image" "DELETE" "/image/proxy/docker.io/${TEST_IMAGE}/${TEST_TAG}" "200"

    # Verify image is gone
    IMAGES_AFTER=$(curl -s -u "$AUTH" "${PORTAL_URL}/images" 2>/dev/null)
    if echo "$IMAGES_AFTER" | grep -q "proxy/docker.io/${TEST_IMAGE}"; then
        log_fail "Proxied image still exists after delete"
    else
        log_pass "Proxied image removed"
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
