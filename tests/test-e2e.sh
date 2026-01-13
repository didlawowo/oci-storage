#!/bin/bash
# End-to-end test script for oci storage
# Tests all functionality as it would be used in a real cluster:
# - OCI protocol (helm push/pull)
# - HTTP API (chart/image management)
# - Proxy (Docker Hub caching)
# - Authentication
# - Backup/Restore
# - Cache management
#
# CRITICAL TEST: Large Layer Blob Download
# This test specifically validates that large blobs (50MB+) are downloaded
# completely without context cancellation. Small blobs (<10KB like configs)
# download too fast to detect the context canceled bug, but large layer blobs
# (100-300MB) will fail if contexts are cancelled prematurely during io.Copy.

set -e

# Configuration
# Use 127.0.0.1 instead of localhost to avoid IPv6 issues on macOS
PORTAL_URL="${PORTAL_URL:-http://127.0.0.1:3030}"
PORTAL_HOST="${PORTAL_HOST:-127.0.0.1:3030}"
AUTH="${PORTAL_AUTH:-admin:admin123}"
AUTH_USER="${AUTH%%:*}"
AUTH_PASS="${AUTH#*:}"
TIMEOUT="${TIMEOUT:-120}"

# Configurable test image for proxy (default: traefik - small multi-arch image)
TEST_IMAGE="${TEST_IMAGE:-traefik}"
TEST_TAG="${TEST_TAG:-latest}"
TEST_TAG_2="${TEST_TAG_2:-v3.2}"

# Set to 1 to keep test data after tests (useful for UI verification)
KEEP_TEST_DATA="${KEEP_TEST_DATA:-0}"

# Skip slow upstream tests
SKIP_UPSTREAM_TESTS="${SKIP_UPSTREAM_TESTS:-0}"

# Test chart location
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART_DIR="${SCRIPT_DIR}/../src/testdata/charts"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

# Test counter
TESTS_PASSED=0
TESTS_FAILED=0
TESTS_SKIPPED=0

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

log_skip() {
    echo -e "${CYAN}[SKIP]${NC} $1"
    TESTS_SKIPPED=$((TESTS_SKIPPED + 1))
}

log_section() {
    echo ""
    echo -e "${BLUE}═══════════════════════════════════════════════════════════════${NC}"
    echo -e "${BLUE}  $1${NC}"
    echo -e "${BLUE}═══════════════════════════════════════════════════════════════${NC}"
}

log_subsection() {
    echo ""
    echo -e "${CYAN}--- $1 ---${NC}"
}

# Test HTTP endpoint
test_endpoint() {
    local description="$1"
    local method="$2"
    local endpoint="$3"
    local expected_code="$4"
    local use_auth="${5:-yes}"

    local url="${PORTAL_URL}${endpoint}"
    local response_code
    local auth_flag=""

    if [ "$use_auth" = "yes" ]; then
        auth_flag="-u $AUTH"
    fi

    if [ "$method" = "HEAD" ]; then
        response_code=$(curl -s -o /dev/null -w "%{http_code}" -I $auth_flag --max-time "$TIMEOUT" "$url" 2>/dev/null)
    elif [ "$method" = "DELETE" ]; then
        response_code=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE $auth_flag --max-time "$TIMEOUT" "$url" 2>/dev/null)
    elif [ "$method" = "POST" ]; then
        response_code=$(curl -s -o /dev/null -w "%{http_code}" -X POST $auth_flag --max-time "$TIMEOUT" "$url" 2>/dev/null)
    else
        response_code=$(curl -s -o /dev/null -w "%{http_code}" $auth_flag --max-time "$TIMEOUT" "$url" 2>/dev/null)
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

# Test endpoint and capture response
test_endpoint_with_body() {
    local description="$1"
    local method="$2"
    local endpoint="$3"
    local expected_code="$4"

    local url="${PORTAL_URL}${endpoint}"
    local response
    local response_code

    response=$(curl -s -w "\n%{http_code}" -u "$AUTH" --max-time "$TIMEOUT" "$url" 2>/dev/null)
    response_code=$(echo "$response" | tail -1)
    response_body=$(echo "$response" | sed '$d')

    if [ "$response_code" = "$expected_code" ]; then
        log_pass "$description (HTTP $response_code)"
        echo "$response_body"
        return 0
    else
        log_fail "$description - Expected $expected_code, got $response_code"
        return 1
    fi
}

echo "════════════════════════════════════════════════════════════════"
echo "              oci storage E2E Test Suite"
echo "════════════════════════════════════════════════════════════════"
echo ""
log_info "Portal URL: $PORTAL_URL"
log_info "Portal Host: $PORTAL_HOST"
log_info "Test images: docker.io/${TEST_IMAGE}:${TEST_TAG}, docker.io/${TEST_IMAGE}:${TEST_TAG_2}"
log_info "Test charts: my-chart (0.1.0, 0.1.1), my-second-chart (0.1.0, 0.2.0)"
echo ""

# ============================================
# SERVER HEALTH CHECK
# ============================================
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

test_endpoint "Health endpoint" "GET" "/health" "200" "no"
test_endpoint "OCI v2 API (with auth)" "GET" "/v2/" "200"

# ============================================
# AUTHENTICATION TESTS
# ============================================
log_section "Authentication Tests"

log_subsection "Anonymous Read Access (allowed by design)"
# GET/HEAD without auth is allowed for proxy/cache functionality
test_endpoint "Anonymous GET /v2/ → 200 (read allowed)" "GET" "/v2/" "200" "no"

log_subsection "Write Operations Require Auth"
# POST without auth should fail
WRITE_NO_AUTH=$(curl -s -o /dev/null -w "%{http_code}" -X POST "${PORTAL_URL}/v2/test/blobs/uploads/" 2>/dev/null)
if [ "$WRITE_NO_AUTH" = "401" ]; then
    log_pass "POST without auth → 401 (HTTP $WRITE_NO_AUTH)"
else
    log_fail "POST without auth should return 401, got $WRITE_NO_AUTH"
fi

# Test invalid credentials on write
INVALID_WRITE=$(curl -s -o /dev/null -w "%{http_code}" -X POST -u "wrong:credentials" "${PORTAL_URL}/v2/test/blobs/uploads/" 2>/dev/null)
if [ "$INVALID_WRITE" = "401" ]; then
    log_pass "POST with invalid credentials → 401 (HTTP $INVALID_WRITE)"
else
    log_fail "POST with invalid credentials should return 401, got $INVALID_WRITE"
fi

# Test malformed auth header on write
MALFORMED_WRITE=$(curl -s -o /dev/null -w "%{http_code}" -X POST -H "Authorization: Basic notbase64!" "${PORTAL_URL}/v2/test/blobs/uploads/" 2>/dev/null)
if [ "$MALFORMED_WRITE" = "401" ]; then
    log_pass "POST with malformed auth → 401 (HTTP $MALFORMED_WRITE)"
else
    log_fail "POST with malformed auth should return 401, got $MALFORMED_WRITE"
fi

log_subsection "Valid Authentication"
test_endpoint "Valid credentials → 200" "GET" "/v2/" "200"
test_endpoint "POST with valid auth → 202" "POST" "/v2/test-auth/blobs/uploads/" "202"

# ============================================
# CLEANUP PREVIOUS TEST DATA
# ============================================
log_section "Cleanup Previous Test Data"

log_info "Removing any existing test charts..."
for chart_name in my-chart my-second-chart; do
    for version in 0.1.0 0.1.1 0.2.0; do
        curl -s -X DELETE -u "$AUTH" "${PORTAL_URL}/chart/${chart_name}/${version}" 2>/dev/null || true
    done
done

log_info "Removing any cached proxy images..."
for tag in "$TEST_TAG" "$TEST_TAG_2"; do
    curl -s -X DELETE -u "$AUTH" "${PORTAL_URL}/image/proxy/docker.io/${TEST_IMAGE}/${tag}" 2>/dev/null || true
    curl -s -X DELETE -u "$AUTH" "${PORTAL_URL}/image/proxy/docker.io/library/${TEST_IMAGE}/${tag}" 2>/dev/null || true
done
log_pass "Previous test data cleaned"

# ============================================
# HELM REPOSITORY TESTS (index.yaml)
# ============================================
log_section "Helm Repository Tests"

log_subsection "index.yaml Endpoint"

# Test index.yaml is accessible (critical for helm repo add)
INDEX_RESPONSE=$(curl -s -w "\n%{http_code}" "${PORTAL_URL}/index.yaml" 2>/dev/null)
INDEX_CODE=$(echo "$INDEX_RESPONSE" | tail -1)
INDEX_BODY=$(echo "$INDEX_RESPONSE" | sed '$d')

if [ "$INDEX_CODE" = "200" ]; then
    log_pass "GET /index.yaml returns 200"

    # Validate it's valid YAML with apiVersion
    if echo "$INDEX_BODY" | grep -q "apiVersion:"; then
        log_pass "index.yaml contains valid Helm repo structure"
    else
        log_fail "index.yaml missing apiVersion field"
    fi
else
    log_fail "GET /index.yaml - Expected 200, got $INDEX_CODE"
fi

# ============================================
# CHART TESTS (HTTP API)
# ============================================
log_section "Chart Tests (HTTP API)"

# Helper function to push a chart via HTTP
push_chart_http() {
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
        log_pass "HTTP Chart upload (${chart_name}:${chart_version})"
        return 0
    else
        log_fail "HTTP Chart upload (${chart_name}:${chart_version}) - got HTTP $PUSH_CODE"
        return 1
    fi
}

log_subsection "Chart Upload via HTTP POST"
push_chart_http "my-chart-0.1.0.tgz" "my-chart" "0.1.0"
push_chart_http "my-chart-0.1.1.tgz" "my-chart" "0.1.1"
push_chart_http "my-second-chart-0.1.0.tgz" "my-second-chart" "0.1.0"
push_chart_http "my-second-chart-0.2.0.tgz" "my-second-chart" "0.2.0"

log_subsection "Chart Listing"
test_endpoint "List charts endpoint" "GET" "/charts" "200"

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

log_subsection "Chart Versions Endpoint"
test_endpoint "Get chart versions" "GET" "/chart/my-chart/versions" "200"

log_subsection "Chart Download"
PULL_DIR=$(mktemp -d)

DOWNLOAD_CODE=$(curl -s -o "${PULL_DIR}/my-chart-0.1.0.tgz" -w "%{http_code}" -u "$AUTH" \
    "${PORTAL_URL}/chart/my-chart/0.1.0" 2>/dev/null)
if [ "$DOWNLOAD_CODE" = "200" ] && [ -s "${PULL_DIR}/my-chart-0.1.0.tgz" ]; then
    log_pass "Chart download (my-chart:0.1.0)"
else
    log_fail "Chart download (my-chart:0.1.0) - got HTTP $DOWNLOAD_CODE"
fi

log_subsection "Chart Details"
test_endpoint "Chart details page" "GET" "/chart/my-chart/0.1.0/details" "200"

log_subsection "Index.yaml Updated After Upload"
INDEX_AFTER=$(curl -s "${PORTAL_URL}/index.yaml" 2>/dev/null)
if echo "$INDEX_AFTER" | grep -q "my-chart"; then
    log_pass "index.yaml contains uploaded chart"
else
    log_fail "index.yaml does not contain uploaded chart"
fi

rm -rf "$PULL_DIR"

# ============================================
# OCI PROTOCOL TESTS (Real helm push/pull)
# ============================================
log_section "OCI Protocol Tests"

# Check if helm is available
if command -v helm &> /dev/null; then
    log_subsection "Helm OCI Push (Real Protocol)"

    # Create temp dir for OCI tests
    OCI_TEST_DIR=$(mktemp -d)
    cp "${CHART_DIR}/my-chart-0.1.0.tgz" "${OCI_TEST_DIR}/"

    # Login to OCI registry (--plain-http for HTTP without TLS)
    echo "$AUTH_PASS" | helm registry login "$PORTAL_HOST" --username "$AUTH_USER" --password-stdin --plain-http 2>/dev/null
    if [ $? -eq 0 ]; then
        log_pass "Helm registry login successful"

        # Push chart via OCI protocol (--plain-http for HTTP)
        PUSH_OUTPUT=$(helm push "${OCI_TEST_DIR}/my-chart-0.1.0.tgz" "oci://${PORTAL_HOST}/charts" --plain-http 2>&1)
        if [ $? -eq 0 ]; then
            log_pass "Helm OCI push successful"
        else
            log_fail "Helm OCI push failed: $PUSH_OUTPUT"
        fi

        log_subsection "Helm OCI Pull (Real Protocol)"

        # Create pull directory
        mkdir -p "${OCI_TEST_DIR}/pulled"

        # Pull chart via OCI protocol (--plain-http for HTTP)
        PULL_OUTPUT=$(helm pull "oci://${PORTAL_HOST}/charts/my-chart" --version 0.1.0 -d "${OCI_TEST_DIR}/pulled" --plain-http 2>&1)
        if [ $? -eq 0 ]; then
            log_pass "Helm OCI pull successful"

            # Verify pulled chart exists
            if [ -f "${OCI_TEST_DIR}/pulled/my-chart-0.1.0.tgz" ]; then
                log_pass "Pulled chart file exists"
            else
                log_fail "Pulled chart file not found"
            fi
        else
            log_fail "Helm OCI pull failed: $PULL_OUTPUT"
        fi

        # Logout
        helm registry logout "$PORTAL_HOST" 2>/dev/null || true
    else
        log_fail "Helm registry login failed"
    fi

    rm -rf "$OCI_TEST_DIR"
else
    log_skip "Helm not installed - skipping OCI protocol tests"
fi

# ============================================
# OCI BLOB OPERATIONS (Low-level protocol)
# ============================================
log_section "OCI Blob Operations"

log_subsection "Blob Upload Flow"

# Test initiate upload
UPLOAD_RESPONSE=$(curl -s -D - -o /dev/null -X POST -u "$AUTH" \
    "${PORTAL_URL}/v2/test-blob/blobs/uploads/" 2>/dev/null)
UPLOAD_CODE=$(echo "$UPLOAD_RESPONSE" | grep "HTTP/" | tail -1 | awk '{print $2}')
UPLOAD_LOCATION=$(echo "$UPLOAD_RESPONSE" | grep -i "Location:" | awk '{print $2}' | tr -d '\r')
UPLOAD_UUID=$(echo "$UPLOAD_RESPONSE" | grep -i "Docker-Upload-UUID:" | awk '{print $2}' | tr -d '\r')

if [ "$UPLOAD_CODE" = "202" ]; then
    log_pass "POST /v2/.../blobs/uploads/ returns 202"

    if [ -n "$UPLOAD_LOCATION" ]; then
        log_pass "Location header present: $UPLOAD_LOCATION"
    else
        log_fail "Location header missing"
    fi

    if [ -n "$UPLOAD_UUID" ]; then
        log_pass "Docker-Upload-UUID header present"

        # Test PATCH blob data
        TEST_BLOB_DATA="test blob content for e2e testing"
        TEST_BLOB_DIGEST=$(echo -n "$TEST_BLOB_DATA" | shasum -a 256 | awk '{print "sha256:"$1}')

        PATCH_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X PATCH -u "$AUTH" \
            -H "Content-Type: application/octet-stream" \
            --data-binary "$TEST_BLOB_DATA" \
            "${PORTAL_URL}/v2/test-blob/blobs/uploads/${UPLOAD_UUID}" 2>/dev/null)

        if [ "$PATCH_CODE" = "202" ]; then
            log_pass "PATCH blob data returns 202"

            # Complete upload
            COMPLETE_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X PUT -u "$AUTH" \
                "${PORTAL_URL}/v2/test-blob/blobs/uploads/${UPLOAD_UUID}?digest=${TEST_BLOB_DIGEST}" 2>/dev/null)

            if [ "$COMPLETE_CODE" = "201" ]; then
                log_pass "PUT complete upload returns 201"

                # Verify blob exists
                log_subsection "Blob Retrieval"
                test_endpoint "HEAD blob exists" "HEAD" "/v2/test-blob/blobs/${TEST_BLOB_DIGEST}" "200"
                test_endpoint "GET blob content" "GET" "/v2/test-blob/blobs/${TEST_BLOB_DIGEST}" "200"
            else
                log_fail "PUT complete upload - Expected 201, got $COMPLETE_CODE"
            fi
        else
            log_fail "PATCH blob data - Expected 202, got $PATCH_CODE"
        fi
    else
        log_fail "Docker-Upload-UUID header missing"
    fi
else
    log_fail "POST /v2/.../blobs/uploads/ - Expected 202, got $UPLOAD_CODE"
fi

# ============================================
# OCI CATALOG AND TAGS
# ============================================
log_section "OCI Catalog and Tags"

test_endpoint "OCI catalog" "GET" "/v2/_catalog" "200"

CATALOG_RESPONSE=$(curl -s -u "$AUTH" "${PORTAL_URL}/v2/_catalog" 2>/dev/null)
if echo "$CATALOG_RESPONSE" | grep -q "repositories"; then
    log_pass "Catalog returns repositories array"
else
    log_fail "Catalog missing repositories field"
fi

test_endpoint "Tags list for chart" "GET" "/v2/my-chart/tags/list" "200"

# ============================================
# CHART DELETION TESTS
# ============================================
log_section "Chart Deletion Tests"

log_subsection "Partial Deletion"
DELETE_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE -u "$AUTH" \
    "${PORTAL_URL}/chart/my-chart/0.1.0" 2>/dev/null)
if [ "$DELETE_CODE" = "200" ]; then
    log_pass "Delete my-chart:0.1.0"
else
    log_fail "Delete my-chart:0.1.0 - got HTTP $DELETE_CODE"
fi

# Verify other version still exists
CHARTS_AFTER=$(curl -s -u "$AUTH" "${PORTAL_URL}/charts" 2>/dev/null)
if echo "$CHARTS_AFTER" | grep -q "0.1.1"; then
    log_pass "my-chart:0.1.1 still exists after deleting 0.1.0"
else
    log_fail "my-chart:0.1.1 was incorrectly deleted"
fi

# ============================================
# PROXY TESTS
# ============================================
log_section "Proxy Tests (Docker Hub Caching)"

if [ "${SKIP_UPSTREAM_TESTS}" != "1" ]; then
    log_subsection "Manifest Proxy (3-segment path)"

    MANIFEST_RESPONSE=$(curl -s -u "$AUTH" "${PORTAL_URL}/v2/proxy/docker.io/${TEST_IMAGE}/manifests/${TEST_TAG}" 2>/dev/null)
    MANIFEST_CODE=$(curl -s -o /dev/null -w "%{http_code}" -u "$AUTH" "${PORTAL_URL}/v2/proxy/docker.io/${TEST_IMAGE}/manifests/${TEST_TAG}" 2>/dev/null)

    if [ "$MANIFEST_CODE" = "200" ]; then
        log_pass "GET manifest ${TEST_IMAGE}:${TEST_TAG} (HTTP 200)"

        # Check if multi-arch
        if echo "$MANIFEST_RESPONSE" | grep -q '"manifests"'; then
            log_pass "Multi-arch manifest detected"

            # Extract and test child manifest
            CHILD_DIGEST=$(echo "$MANIFEST_RESPONSE" | grep -o '"digest":"sha256:[a-f0-9]*"' | head -1 | cut -d'"' -f4)
            if [ -n "$CHILD_DIGEST" ]; then
                log_info "Testing child manifest: ${CHILD_DIGEST:0:20}..."

                CHILD_CODE=$(curl -s -o /dev/null -w "%{http_code}" -u "$AUTH" \
                    "${PORTAL_URL}/v2/proxy/docker.io/${TEST_IMAGE}/manifests/${CHILD_DIGEST}" 2>/dev/null)

                if [ "$CHILD_CODE" = "200" ]; then
                    log_pass "Child manifest fetch by digest (HTTP 200)"
                else
                    log_fail "Child manifest fetch - got HTTP $CHILD_CODE"
                fi
            fi
        else
            log_info "Single-arch manifest"
        fi
    else
        log_fail "GET manifest ${TEST_IMAGE}:${TEST_TAG} - got HTTP $MANIFEST_CODE"
    fi

    log_subsection "Manifest Proxy (4-segment path)"
    test_endpoint "GET manifest 4-segment (library/${TEST_IMAGE})" "GET" "/v2/proxy/docker.io/library/${TEST_IMAGE}/manifests/${TEST_TAG}" "200"

    log_subsection "Second Tag Caching"
    test_endpoint "GET manifest ${TEST_IMAGE}:${TEST_TAG_2}" "GET" "/v2/proxy/docker.io/${TEST_IMAGE}/manifests/${TEST_TAG_2}" "200"

    log_subsection "Blob Proxy (Config - Small)"
    # Get a blob digest from the manifest
    if [ -n "$MANIFEST_RESPONSE" ]; then
        # Try to get config digest for blob test
        CONFIG_DIGEST=$(echo "$MANIFEST_RESPONSE" | grep -o '"config"[^}]*"digest":"sha256:[a-f0-9]*"' | grep -o 'sha256:[a-f0-9]*' | head -1)
        if [ -n "$CONFIG_DIGEST" ]; then
            log_info "Testing blob proxy with config: ${CONFIG_DIGEST:0:20}..."
            test_endpoint "GET blob via proxy" "GET" "/v2/proxy/docker.io/${TEST_IMAGE}/blobs/${CONFIG_DIGEST}" "200"
            test_endpoint "HEAD blob via proxy" "HEAD" "/v2/proxy/docker.io/${TEST_IMAGE}/blobs/${CONFIG_DIGEST}" "200"
        else
            log_info "No config digest found in manifest, skipping blob test"
        fi
    fi

    log_subsection "Blob Proxy (Layer - Large) - CRITICAL TEST"
    # Extract FIRST LAYER blob (typically 50-300MB) - this tests the context canceled bug
    if [ -n "$MANIFEST_RESPONSE" ]; then
        # Try to get first layer digest - parse layers array properly
        LAYER_DIGEST=$(echo "$MANIFEST_RESPONSE" | grep -o '"layers"[[:space:]]*:[[:space:]]*\[' -A 50 | grep -o '"digest"[[:space:]]*:[[:space:]]*"sha256:[a-f0-9]*"' | head -1 | grep -o 'sha256:[a-f0-9]*')
        LAYER_SIZE=$(echo "$MANIFEST_RESPONSE" | grep "$LAYER_DIGEST" -A 2 -B 2 | grep -o '"size"[[:space:]]*:[[:space:]]*[0-9]*' | grep -o '[0-9]*' | head -1)

        if [ -n "$LAYER_DIGEST" ]; then
            LAYER_SIZE_MB=$((LAYER_SIZE / 1024 / 1024))
            log_info "Testing LARGE layer blob: ${LAYER_DIGEST:0:20}... (${LAYER_SIZE_MB}MB)"

            # Download with extended timeout and capture to temp file to verify completeness
            DOWNLOAD_FILE="/tmp/test-layer-blob-$$.bin"
            DOWNLOAD_START=$(date +%s)

            # Use longer timeout for large blobs (10 minutes max)
            BLOB_CODE=$(curl -s -o "$DOWNLOAD_FILE" -w "%{http_code}" -u "$AUTH" \
                --max-time 600 \
                "${PORTAL_URL}/v2/proxy/docker.io/${TEST_IMAGE}/blobs/${LAYER_DIGEST}" 2>/dev/null)

            DOWNLOAD_END=$(date +%s)
            DOWNLOAD_TIME=$((DOWNLOAD_END - DOWNLOAD_START))

            if [ "$BLOB_CODE" = "200" ]; then
                log_pass "Large layer blob HTTP 200 (${DOWNLOAD_TIME}s)"

                # Verify downloaded file size matches expected size
                if [ -f "$DOWNLOAD_FILE" ]; then
                    # macOS uses stat -f%z, Linux uses stat -c%s
                    ACTUAL_SIZE=$(stat -f%z "$DOWNLOAD_FILE" 2>/dev/null || stat -c%s "$DOWNLOAD_FILE" 2>/dev/null || echo "0")

                    if [ "$ACTUAL_SIZE" = "$LAYER_SIZE" ]; then
                        log_pass "Layer blob size verified: ${LAYER_SIZE_MB}MB (no truncation)"
                    elif [ "$ACTUAL_SIZE" -gt 0 ]; then
                        ACTUAL_MB=$((ACTUAL_SIZE / 1024 / 1024))
                        log_fail "Layer blob TRUNCATED! Expected ${LAYER_SIZE_MB}MB, got ${ACTUAL_MB}MB (context canceled?)"
                    else
                        log_fail "Layer blob download resulted in empty file (context canceled?)"
                    fi

                    rm -f "$DOWNLOAD_FILE"
                else
                    log_fail "Layer blob download file not created"
                fi

                # Verify blob is cached on server (wait for cache write to complete)
                sleep 2
                CACHE_CHECK=$(curl -s -o /dev/null -w "%{http_code}" -I -u "$AUTH" \
                    "${PORTAL_URL}/v2/proxy/docker.io/${TEST_IMAGE}/blobs/${LAYER_DIGEST}" 2>/dev/null)

                if [ "$CACHE_CHECK" = "200" ]; then
                    log_pass "Layer blob is cached (subsequent requests will be fast)"
                else
                    log_fail "Layer blob HEAD check failed after download (cache issue?)"
                fi
            else
                log_fail "Large layer blob download failed (HTTP $BLOB_CODE) - this indicates context canceled bug!"
            fi
        else
            log_info "No layer digest found in manifest (single-arch or empty layers?)"

            # If manifest list, try to get layer from first child manifest
            if echo "$MANIFEST_RESPONSE" | grep -q '"manifests"'; then
                log_info "Manifest list detected, extracting first child manifest for layer test..."
                CHILD_DIGEST=$(echo "$MANIFEST_RESPONSE" | grep -o '"digest":"sha256:[a-f0-9]*"' | head -1 | cut -d'"' -f4)

                if [ -n "$CHILD_DIGEST" ]; then
                    CHILD_MANIFEST=$(curl -s -u "$AUTH" \
                        "${PORTAL_URL}/v2/proxy/docker.io/${TEST_IMAGE}/manifests/${CHILD_DIGEST}" 2>/dev/null)

                    LAYER_DIGEST=$(echo "$CHILD_MANIFEST" | grep -o '"layers"[[:space:]]*:[[:space:]]*\[' -A 50 | grep -o '"digest"[[:space:]]*:[[:space:]]*"sha256:[a-f0-9]*"' | head -1 | grep -o 'sha256:[a-f0-9]*')
                    LAYER_SIZE=$(echo "$CHILD_MANIFEST" | grep "$LAYER_DIGEST" -A 2 -B 2 | grep -o '"size"[[:space:]]*:[[:space:]]*[0-9]*' | grep -o '[0-9]*' | head -1)

                    if [ -n "$LAYER_DIGEST" ] && [ -n "$LAYER_SIZE" ]; then
                        LAYER_SIZE_MB=$((LAYER_SIZE / 1024 / 1024))
                        log_info "Found layer in child manifest: ${LAYER_DIGEST:0:20}... (${LAYER_SIZE_MB}MB)"

                        # Same download test as above
                        DOWNLOAD_FILE="/tmp/test-layer-blob-$$.bin"
                        BLOB_CODE=$(curl -s -o "$DOWNLOAD_FILE" -w "%{http_code}" -u "$AUTH" \
                            --max-time 600 \
                            "${PORTAL_URL}/v2/proxy/docker.io/${TEST_IMAGE}/blobs/${LAYER_DIGEST}" 2>/dev/null)

                        if [ "$BLOB_CODE" = "200" ] && [ -f "$DOWNLOAD_FILE" ]; then
                            ACTUAL_SIZE=$(stat -f%z "$DOWNLOAD_FILE" 2>/dev/null || stat -c%s "$DOWNLOAD_FILE" 2>/dev/null || echo "0")

                            if [ "$ACTUAL_SIZE" = "$LAYER_SIZE" ]; then
                                log_pass "Child manifest layer blob verified: ${LAYER_SIZE_MB}MB"
                            else
                                ACTUAL_MB=$((ACTUAL_SIZE / 1024 / 1024))
                                log_fail "Child manifest layer blob TRUNCATED! Expected ${LAYER_SIZE_MB}MB, got ${ACTUAL_MB}MB"
                            fi

                            rm -f "$DOWNLOAD_FILE"
                        else
                            log_fail "Child manifest layer blob download failed (HTTP $BLOB_CODE)"
                        fi
                    else
                        log_skip "Could not extract layer from child manifest"
                    fi
                else
                    log_skip "Could not extract child digest from manifest list"
                fi
            else
                log_skip "No layers available for large blob test"
            fi
        fi
    else
        log_skip "No manifest response available for large blob test"
    fi

    log_subsection "Cached Images Verification"

    IMAGE_RESPONSE=$(curl -s -u "$AUTH" "${PORTAL_URL}/cache/images" 2>/dev/null)

    if echo "$IMAGE_RESPONSE" | grep -q "proxy/docker.io/${TEST_IMAGE}"; then
        log_pass "Proxied image appears in /images list"
    else
        log_fail "Proxied image not in /images list"
    fi

    # Count tags
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

    log_subsection "Image Details Endpoint"
    # Small delay to allow async cache writes to complete
    sleep 1
    test_endpoint "Image details (${TEST_TAG})" "GET" "/image/proxy/docker.io/${TEST_IMAGE}/${TEST_TAG}/details" "200"
    test_endpoint "Image details (${TEST_TAG_2})" "GET" "/image/proxy/docker.io/${TEST_IMAGE}/${TEST_TAG_2}/details" "200"

    log_subsection "Image Deletion"
    test_endpoint "Delete ${TEST_IMAGE}:${TEST_TAG}" "DELETE" "/image/proxy/docker.io/${TEST_IMAGE}/${TEST_TAG}" "200"

    # Verify other tag still exists
    IMAGES_PARTIAL=$(curl -s -u "$AUTH" "${PORTAL_URL}/images" 2>/dev/null)
    if echo "$IMAGES_PARTIAL" | grep -q "${TEST_TAG_2}"; then
        log_pass "${TEST_IMAGE}:${TEST_TAG_2} still exists after deleting ${TEST_TAG}"
    else
        log_fail "${TEST_IMAGE}:${TEST_TAG_2} was incorrectly deleted"
    fi
else
    log_skip "Upstream proxy tests (SKIP_UPSTREAM_TESTS=1)"
fi

# ============================================
# CACHE MANAGEMENT TESTS
# ============================================
log_section "Cache Management"

test_endpoint "Cache status endpoint" "GET" "/cache/status" "200"

CACHE_STATUS=$(curl -s -u "$AUTH" "${PORTAL_URL}/cache/status" 2>/dev/null)
if echo "$CACHE_STATUS" | grep -q "totalSize\|maxSize\|usagePercent"; then
    log_pass "Cache status contains expected fields"
else
    log_fail "Cache status missing expected fields"
fi

test_endpoint "List cached images" "GET" "/cache/images" "200"

# ============================================
# BACKUP/RESTORE TESTS
# ============================================
log_section "Backup/Restore"

test_endpoint "Backup status endpoint" "GET" "/backup/status" "200"

BACKUP_STATUS=$(curl -s "${PORTAL_URL}/backup/status" 2>/dev/null)
if echo "$BACKUP_STATUS" | grep -q "enabled"; then
    log_pass "Backup status returns enabled field"

    BACKUP_ENABLED=$(echo "$BACKUP_STATUS" | grep -o '"enabled":[^,}]*' | cut -d: -f2)
    if [ "$BACKUP_ENABLED" = "true" ]; then
        log_info "Backup is enabled, testing backup endpoint..."
        # Note: We don't actually trigger backup in tests to avoid side effects
        # Just verify the endpoint exists
        BACKUP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST -u "$AUTH" "${PORTAL_URL}/backup" 2>/dev/null)
        if [ "$BACKUP_CODE" = "200" ] || [ "$BACKUP_CODE" = "500" ]; then
            # 500 is acceptable if cloud provider not configured
            log_pass "Backup endpoint is accessible (HTTP $BACKUP_CODE)"
        else
            log_fail "Backup endpoint returned unexpected code: $BACKUP_CODE"
        fi
    else
        log_info "Backup is disabled, skipping backup trigger test"
    fi
else
    log_fail "Backup status missing enabled field"
fi

# ============================================
# CONFIG ENDPOINT
# ============================================
log_section "Configuration"

test_endpoint "Config endpoint" "GET" "/config" "200"

# ============================================
# CLEANUP
# ============================================
log_section "Cleanup"

if [ "${KEEP_TEST_DATA}" = "1" ]; then
    log_info "Keeping test data (KEEP_TEST_DATA=1)"
    # Re-upload deleted chart for complete data
    push_chart_http "my-chart-0.1.0.tgz" "my-chart" "0.1.0" 2>/dev/null || true
    if [ "${SKIP_UPSTREAM_TESTS}" != "1" ]; then
        # Re-fetch deleted image
        curl -s -u "$AUTH" "${PORTAL_URL}/v2/proxy/docker.io/${TEST_IMAGE}/manifests/${TEST_TAG}" >/dev/null 2>&1 || true
    fi
else
    log_info "Cleaning up all test data..."

    # Delete remaining charts (suppress output - some may not exist)
    for chart_name in my-chart my-second-chart; do
        for version in 0.1.0 0.1.1 0.2.0; do
            curl -s -X DELETE -u "$AUTH" "${PORTAL_URL}/chart/${chart_name}/${version}" >/dev/null 2>&1 || true
        done
    done

    # Delete remaining images (suppress output)
    if [ "${SKIP_UPSTREAM_TESTS}" != "1" ]; then
        curl -s -X DELETE -u "$AUTH" "${PORTAL_URL}/image/proxy/docker.io/${TEST_IMAGE}/${TEST_TAG_2}" >/dev/null 2>&1 || true
    fi

    # Verify cleanup
    CHARTS_FINAL=$(curl -s -u "$AUTH" "${PORTAL_URL}/charts" 2>/dev/null)
    if echo "$CHARTS_FINAL" | grep -q "my-chart\|my-second-chart"; then
        log_fail "Some test charts still exist after cleanup"
    else
        log_pass "All test charts removed"
    fi
fi

# ============================================
# RESULTS
# ============================================
echo ""
echo "════════════════════════════════════════════════════════════════"
echo "                      Test Results"
echo "════════════════════════════════════════════════════════════════"
echo -e "${GREEN}Passed:  $TESTS_PASSED${NC}"
echo -e "${RED}Failed:  $TESTS_FAILED${NC}"
echo -e "${CYAN}Skipped: $TESTS_SKIPPED${NC}"
echo ""

TOTAL_TESTS=$((TESTS_PASSED + TESTS_FAILED))
if [ $TOTAL_TESTS -gt 0 ]; then
    PASS_RATE=$((TESTS_PASSED * 100 / TOTAL_TESTS))
    echo "Pass rate: ${PASS_RATE}%"
fi
echo ""

if [ $TESTS_FAILED -gt 0 ]; then
    echo -e "${RED}Some tests failed!${NC}"
    exit 1
else
    echo -e "${GREEN}All tests passed!${NC}"
    exit 0
fi