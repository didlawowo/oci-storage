#!/bin/bash
# Test script for Docker proxy functionality
# Can be run locally or in CI

set -e

# Configuration
PORTAL_URL="${PORTAL_URL:-http://localhost:3030}"
AUTH="${PORTAL_AUTH:-admin:admin123}"
TIMEOUT="${TIMEOUT:-120}"
# Configurable test image (default: traefik:latest)
TEST_IMAGE="${TEST_IMAGE:-traefik}"
TEST_TAG="${TEST_TAG:-latest}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
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

# Test function
test_endpoint() {
    local description="$1"
    local method="$2"
    local endpoint="$3"
    local expected_code="$4"

    local url="${PORTAL_URL}${endpoint}"
    local response_code

    if [ "$method" = "HEAD" ]; then
        response_code=$(curl -s -o /dev/null -w "%{http_code}" -I -u "$AUTH" --max-time "$TIMEOUT" "$url" 2>/dev/null)
    else
        response_code=$(curl -s -o /dev/null -w "%{http_code}" -u "$AUTH" --max-time "$TIMEOUT" "$url" 2>/dev/null)
    fi
    # Handle curl failures (timeout, connection refused, etc.)
    if [ -z "$response_code" ] || [ "$response_code" = "000" ]; then
        response_code="000"
    fi

    if [ "$response_code" = "$expected_code" ]; then
        log_pass "$description (HTTP $response_code)"
    else
        log_fail "$description - Expected $expected_code, got $response_code"
    fi
}

echo "========================================"
echo "  oci storage Proxy Test Suite"
echo "========================================"
echo ""
log_info "Testing portal at: $PORTAL_URL"
log_info "Test image: docker.io/${TEST_IMAGE}:${TEST_TAG}"
echo ""

# Wait for server if needed
log_info "Checking server availability..."
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

echo ""
echo "--- Basic API Tests ---"
test_endpoint "Health endpoint" "GET" "/health" "200"
test_endpoint "OCI v2 base endpoint (auth required)" "GET" "/v2/" "200"

echo ""
echo "--- Local Image Tests ---"
test_endpoint "List images endpoint" "GET" "/images" "200"
test_endpoint "Cache status endpoint" "GET" "/cache/status" "200"

echo ""
echo "--- Proxy Route Tests (2 segments) ---"
test_endpoint "OCI catalog" "GET" "/v2/_catalog" "200"
# Note: 2-segment paths like /v2/nginx/manifests/alpine are for local images

echo ""
echo "--- Proxy Route Tests (3 segments: proxy/registry/image) ---"
# These tests require actual upstream connectivity and may take time
log_info "Testing 3-segment proxy paths with ${TEST_IMAGE}..."
# Note: HEAD with tag returns 404 (allows push), HEAD with digest would proxy
test_endpoint "HEAD manifest 3seg with tag (expected 404 - allows push)" "HEAD" "/v2/proxy/docker.io/${TEST_IMAGE}/manifests/${TEST_TAG}" "404"

echo ""
echo "--- Proxy Route Tests (4 segments: proxy/registry/namespace/image) ---"
log_info "Testing 4-segment proxy paths with library/${TEST_IMAGE}..."
# Note: HEAD with tag returns 404 (allows push), HEAD with digest would proxy
test_endpoint "HEAD manifest 4seg with tag (expected 404 - allows push)" "HEAD" "/v2/proxy/docker.io/library/${TEST_IMAGE}/manifests/${TEST_TAG}" "404"

# Optional: Test actual proxy GET (requires network access to Docker Hub)
# These are slow and may timeout - skip in CI with SKIP_UPSTREAM_TESTS=1
if [ "${SKIP_UPSTREAM_TESTS:-0}" != "1" ]; then
    echo ""
    echo "--- Upstream Proxy Tests (requires Docker Hub access) ---"
    log_info "Testing upstream proxy with ${TEST_IMAGE}:${TEST_TAG} (this may take a while)..."
    test_endpoint "GET manifest via proxy (docker.io/${TEST_IMAGE})" "GET" "/v2/proxy/docker.io/${TEST_IMAGE}/manifests/${TEST_TAG}" "200"

    # Verify image shows up in /images endpoint with correct metadata
    echo ""
    log_info "Verifying image metadata..."
    IMAGE_RESPONSE=$(curl -s -u "$AUTH" "${PORTAL_URL}/images" 2>/dev/null)

    # Check if image name appears in response
    if echo "$IMAGE_RESPONSE" | grep -q "proxy/docker.io/${TEST_IMAGE}"; then
        log_pass "Image appears in /images endpoint"
    else
        log_fail "Image not found in /images endpoint"
    fi

    # Check if size is non-zero (verify size calculation fix)
    SIZE=$(echo "$IMAGE_RESPONSE" | grep -o '"size":[0-9]*' | head -1 | cut -d: -f2)
    if [ -n "$SIZE" ] && [ "$SIZE" != "0" ]; then
        log_pass "Image size is non-zero: $SIZE bytes"
    else
        log_fail "Image size is zero or missing (size=$SIZE)"
    fi
else
    echo ""
    log_info "Skipping upstream proxy tests (SKIP_UPSTREAM_TESTS=1)"
fi

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
