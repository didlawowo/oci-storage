#!/bin/bash
# Test script for Docker proxy functionality
# Can be run locally or in CI

set -e

# Configuration
PORTAL_URL="${PORTAL_URL:-http://localhost:3030}"
AUTH="${PORTAL_AUTH:-admin:admin123}"
TIMEOUT="${TIMEOUT:-30}"

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
    ((TESTS_PASSED++))
}

log_fail() {
    echo -e "${RED}[FAIL]${NC} $1"
    ((TESTS_FAILED++))
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
        response_code=$(curl -s -o /dev/null -w "%{http_code}" -X HEAD -u "$AUTH" --max-time "$TIMEOUT" "$url" 2>/dev/null || echo "000")
    else
        response_code=$(curl -s -o /dev/null -w "%{http_code}" -u "$AUTH" --max-time "$TIMEOUT" "$url" 2>/dev/null || echo "000")
    fi

    if [ "$response_code" = "$expected_code" ]; then
        log_pass "$description (HTTP $response_code)"
    else
        log_fail "$description - Expected $expected_code, got $response_code"
    fi
}

echo "========================================"
echo "  Helm Portal Proxy Test Suite"
echo "========================================"
echo ""
log_info "Testing portal at: $PORTAL_URL"
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
log_info "Testing 3-segment proxy paths (may take a moment)..."
test_endpoint "HEAD manifest 3seg (proxy/docker.io/nginx)" "HEAD" "/v2/proxy/docker.io/nginx/manifests/alpine" "200"
test_endpoint "GET manifest 3seg (proxy/docker.io/nginx)" "GET" "/v2/proxy/docker.io/nginx/manifests/alpine" "200"

echo ""
echo "--- Proxy Route Tests (4 segments: proxy/registry/namespace/image) ---"
log_info "Testing 4-segment proxy paths for Docker Hub library images..."
test_endpoint "HEAD manifest 4seg (proxy/docker.io/library/nginx)" "HEAD" "/v2/proxy/docker.io/library/nginx/manifests/alpine" "200"
test_endpoint "GET manifest 4seg (proxy/docker.io/library/nginx)" "GET" "/v2/proxy/docker.io/library/nginx/manifests/alpine" "200"

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
