/**
 * oci storage - Main JavaScript File
 * Ce fichier contient toutes les fonctionnalités JavaScript pour le portail Helm Charts
 */

// ⚙️ Gestion des modales
/**
 * Affiche la modale avec un message personnalisé
 * @param {string} message - Le message à afficher dans la modale
 * @param {boolean} isError - Indique s'il s'agit d'une erreur (rouge) ou d'un succès (vert)
 */
function showModal(message, isError = true) {
  // ⚠️ Debug - Vérifier si la fonction est appelée
  console.log("showModal called:", message, isError);

  const modal = document.getElementById("errorModal");
  const content = document.getElementById("errorModalContent");
  const title = modal.querySelector("h3");

  // Mettre à jour le contenu et l'apparence
  content.textContent = message;

  if (isError) {
    title.textContent = "Erreur";
    title.classList.remove("text-green-600");
    title.classList.add("text-red-600");
  } else {
    title.textContent = "Succès";
    title.classList.remove("text-red-600");
    title.classList.add("text-green-600");
  }

  // Afficher la modale - s'assurer qu'elle est visible
  modal.classList.remove("hidden");
  modal.style.display = "flex";

  // ⚠️ Debug - Vérifier l'état de la modale après tentative d'affichage
  console.log("Modal state after show:", modal.classList, modal.style.display);
}

/**
 * Ferme la modale
 */
function closeErrorModal() {
  const modal = document.getElementById("errorModal");
  modal.classList.add("hidden");
  modal.style.display = "none";
}

// 🔄 Gestion des API et requêtes
/**
 * Gestionnaire d'erreur générique pour les appels fetch
 * @param {Response} response - La réponse de l'API
 * @returns {Promise} - Retourne les données JSON ou lève une erreur
 */
function handleFetchError(response) {
  if (!response.ok) {
    return response.json().then((data) => {
      throw new Error(data.error || "Une erreur s'est produite");
    });
  }
  return response.json();
}

/**
 * Récupère les versions d'un chart spécifique
 * @param {string} name - Le nom du chart
 * @returns {Promise<Array>} - Les versions du chart ou un tableau vide en cas d'erreur
 */
async function fetchChartVersions(name) {
  try {
    const response = await fetch(`/chart/${name}/versions`);
    if (response.ok) {
      return await response.json();
    }
    return [];
  } catch (error) {
    console.error("Error fetching versions:", error);
    return [];
  }
}

// 💾 Fonctionnalités de sauvegarde
/**
 * Effectue une sauvegarde du système
 * @returns {Promise<void>}
 */
async function performBackup() {
  try {
    const response = await fetch("/backup", {
      method: "POST",
    });

    const data = await handleFetchError(response);
    showModal("Backup réalisé avec succès: " + data.message, false);
  } catch (error) {
    console.error("Erreur:", error);
    showModal("Erreur lors du backup: " + error.message);
  }
}

/**
 * Vérifie si la fonctionnalité de backup est activée
 * @returns {Promise<void>}
 */
async function checkBackupStatus() {
  try {
    const response = await fetch("/backup/status");
    const data = await response.json();

    const backupForm = document.getElementById("backupButton").closest("form");
    if (!data.enabled) {
      backupForm.style.display = "none";
    }
  } catch (error) {
    console.error("Error fetching backup status:", error);
  }
}

// 📊 Gestion des charts
/**
 * Bascule vers une autre version d'un chart
 * @param {string} chartName - Le nom du chart
 * @param {string} version - La version à afficher
 */
function switchVersion(chartName, version) {
  const card = document.querySelector(`[data-chart-name="${chartName}"]`);
  if (!card) return;

  // Mise à jour des URLs des actions
  const infoLink = card.querySelector(".icon-info").parentElement;
  const downloadLink = card.querySelector(".icon-download").parentElement;
  const deleteLink = card.querySelector(".icon-delete").parentElement;

  infoLink.href = `/chart/${chartName}/${version}/details`;
  downloadLink.href = `/chart/${chartName}/${version}`;

  // Réinitialiser le gestionnaire d'événements pour le bouton de suppression
  deleteLink.onclick = function () {
    deleteChart(chartName, version);
  };

  // Si nous avons des données de version en cache, mettre à jour les détails
  if (window.chartVersions && window.chartVersions[chartName]) {
    const currentVersion = window.chartVersions[chartName].find(
      (v) => v.version === version
    );
    if (currentVersion) {
      const appVersionElem = card.querySelector(".version-details p span");
      const descriptionElem = card.querySelector(".description");

      if (appVersionElem && appVersionElem.nextSibling) {
        appVersionElem.nextSibling.textContent =
          " " + (currentVersion.appVersion || "N/A");
      }

      if (descriptionElem) {
        descriptionElem.textContent = currentVersion.description || "";
      }
    }
  }
}

/**
 * Supprime une version spécifique d'un chart
 * @param {string} name - Le nom du chart
 * @param {string} version - La version à supprimer
 * @returns {Promise<void>}
 */
async function deleteChart(name, version) {
  if (!confirm("Are you sure you want to delete this version?")) {
    return;
  }

  try {
    const response = await fetch(`/chart/${name}/${version}`, {
      method: "DELETE",
    });

    if (!response.ok) {
      const errorText = await response.text();
      throw new Error(errorText || "Failed to delete chart");
    }

    // Trouver la carte à mettre à jour
    const chartCard = document.querySelector(`[data-chart-name="${name}"]`);
    if (chartCard) {
      // Récupérer les versions mises à jour
      const updatedVersions = await fetchChartVersions(name);
      if (updatedVersions.length === 0) {
        // Si plus de versions, supprimer la carte
        chartCard.remove();
        showModal(`Chart ${name} a été complètement supprimé`, false);
      } else {
        // Sinon, mettre à jour l'interface si nécessaire
        updateChart(chartCard, name, updatedVersions);
        showModal(
          `Version ${version} du chart ${name} supprimée avec succès`,
          false
        );
      }
    }
  } catch (error) {
    console.error("Error:", error);
    showModal(`Erreur lors de la suppression: ${error.message}`);
  }
}

/**
 * Met à jour l'affichage d'une carte chart après modification des versions
 * @param {HTMLElement} cardElement - L'élément DOM de la carte
 * @param {string} chartName - Le nom du chart
 * @param {Array} versions - Les versions disponibles
 */
function updateChart(cardElement, chartName, versions) {
  // Mise à jour du sélecteur de version si présent
  const select = cardElement.querySelector("select");
  if (select) {
    // Sauvegarder l'ancienne valeur sélectionnée si possible
    const oldValue = select.value;

    // Créer les nouvelles options
    select.innerHTML = versions
      .map((v) => `<option value="${v.version}">Version: ${v.version}</option>`)
      .join("");

    // Sélectionner la première version disponible
    const newVersion = versions[0].version;
    select.value = newVersion;

    // Mettre à jour les détails affichés
    switchVersion(chartName, newVersion);
  }

  // Stocker les versions dans le cache
  if (!window.chartVersions) window.chartVersions = {};
  window.chartVersions[chartName] = versions;
}

// 🐳 Docker Images Management
/**
 * Current active tab - Default to images (proxy cache)
 */
let activeTab = "images";

/**
 * Switch between charts and images tabs
 * @param {string} tab - The tab to show ('charts' or 'images')
 */
function showTab(tab) {
  activeTab = tab;

  const chartsSection = document.getElementById("chartsSection");
  const imagesSection = document.getElementById("imagesSection");
  const securitySection = document.getElementById("securitySection");
  const chartsTab = document.getElementById("chartsTab");
  const imagesTab = document.getElementById("imagesTab");
  const securityTab = document.getElementById("securityTab");
  const uploadForm = document.getElementById("uploadForm");

  // Hide all sections
  chartsSection.style.display = "none";
  imagesSection.style.display = "none";
  if (securitySection) securitySection.style.display = "none";

  // Deactivate all tabs
  chartsTab.classList.remove("active", "bg-blue-700");
  imagesTab.classList.remove("active", "bg-blue-700");
  if (securityTab) securityTab.classList.remove("active", "bg-blue-700");

  if (uploadForm) uploadForm.style.display = "none";

  if (tab === "charts") {
    chartsSection.style.display = "block";
    chartsTab.classList.add("active", "bg-blue-700");
    if (uploadForm) uploadForm.style.display = "flex";
  } else if (tab === "images") {
    imagesSection.style.display = "block";
    imagesTab.classList.add("active", "bg-blue-700");
    loadDockerImages();
    loadCacheStatus();
  } else if (tab === "security") {
    if (securitySection) securitySection.style.display = "block";
    if (securityTab) securityTab.classList.add("active", "bg-blue-700");
    loadScanSummary();
    loadScanDecisions("pending");
  }
}

/**
 * Load and display cache status
 */
async function loadCacheStatus() {
  try {
    const response = await fetch("/cache/status");
    const data = await response.json();

    const usageText = document.getElementById("cacheUsageText");
    const progressBar = document.getElementById("cacheProgressBar");
    const itemCount = document.getElementById("cacheItemCount");
    const proxyStatus = document.getElementById("cacheProxyStatus");

    if (!data.enabled) {
      usageText.textContent = "(Proxy disabled)";
      progressBar.style.width = "0%";
      itemCount.textContent = "Proxy not enabled";
      proxyStatus.textContent = "Proxy: disabled";
      return;
    }

    // Format sizes
    const formatSize = (bytes) => {
      if (!bytes || bytes === 0) return "0 MB";
      const mb = bytes / (1024 * 1024);
      if (mb >= 1024) {
        return (mb / 1024).toFixed(2) + " GB";
      }
      return mb.toFixed(2) + " MB";
    };

    const usedSize = formatSize(data.totalSize);
    const maxSize = formatSize(data.maxSize);
    const percent = data.usagePercent ? data.usagePercent.toFixed(1) : 0;

    usageText.textContent = `${usedSize} / ${maxSize} (${percent}%)`;
    progressBar.style.width = `${Math.min(percent, 100)}%`;
    itemCount.textContent = `${data.itemCount || 0} images cached`;
    proxyStatus.textContent = "Proxy: enabled";

    // Change color based on usage
    progressBar.classList.remove(
      "bg-purple-600",
      "bg-yellow-500",
      "bg-red-600"
    );
    if (percent > 90) {
      progressBar.classList.add("bg-red-600");
    } else if (percent > 70) {
      progressBar.classList.add("bg-yellow-500");
    } else {
      progressBar.classList.add("bg-purple-600");
    }
  } catch (error) {
    console.error("Error loading cache status:", error);
    document.getElementById("cacheUsageText").textContent = "(Error loading)";
  }
}

/**
 * Purge the entire cache
 */
async function purgeCache() {
  if (
    !confirm(
      "Are you sure you want to purge the entire image cache? This cannot be undone."
    )
  ) {
    return;
  }

  try {
    const response = await fetch("/cache/purge", { method: "POST" });
    const data = await response.json();

    if (response.ok) {
      showModal("Cache purged successfully", false);
      loadDockerImages();
      loadCacheStatus();
    } else {
      showModal("Error: " + (data.error || "Failed to purge cache"), true);
    }
  } catch (error) {
    console.error("Error purging cache:", error);
    showModal("Error purging cache: " + error.message, true);
  }
}

// Global state for image filtering, sorting, and view mode
let allDockerImages = [];
let currentImageFilter = 'all';
let currentSortOrder = 'date-desc';
let currentViewMode = 'list';

/**
 * Fetch and display all Docker images (pushed + cached from proxy)
 */
async function loadDockerImages() {
  const container = document.getElementById("imagesContainer");
  const noImagesMessage = document.getElementById("noImagesMessage");

  try {
    // Fetch both pushed images and cached proxy images
    const [pushedResponse, cachedResponse] = await Promise.all([
      fetch("/images"),
      fetch("/cache/images")
    ]);

    const pushedData = await pushedResponse.json();
    const cachedData = await cachedResponse.json();

    // Convert pushed images to a common format
    const pushedImages = [];
    if (pushedData.images) {
      for (const group of pushedData.images) {
        for (const tag of group.tags) {
          pushedImages.push({
            name: tag.name || group.name,
            tag: tag.tag,
            size: tag.size,
            digest: tag.digest,
            created: tag.created,
            sourceRegistry: "local",
            cachedAt: tag.created,
            lastAccessed: tag.created,
            isPushed: true // Mark as directly pushed
          });
        }
      }
    }

    // Mark cached images as proxied
    const cachedImages = (cachedData.images || []).map(img => ({
      ...img,
      isPushed: false
    }));

    // Combine and deduplicate (prefer pushed over cached if same name:tag)
    const imageMap = new Map();

    // Add cached images first
    for (const img of cachedImages) {
      const key = `${img.name}:${img.tag}`;
      imageMap.set(key, img);
    }

    // Pushed images override cached
    for (const img of pushedImages) {
      const key = `${img.name}:${img.tag}`;
      imageMap.set(key, img);
    }

    // Store all images globally for filtering
    allDockerImages = Array.from(imageMap.values());

    // Sort by lastAccessed/created (most recent first)
    allDockerImages.sort((a, b) => {
      const dateA = new Date(a.lastAccessed || a.created || 0);
      const dateB = new Date(b.lastAccessed || b.created || 0);
      return dateB - dateA;
    });

    // Apply current filter and render
    renderFilteredImages();
  } catch (error) {
    console.error("Error loading Docker images:", error);
    container.innerHTML = `
            <div class="col-span-full text-center text-red-500">
                <i class="material-icons text-4xl">error</i>
                <p>Failed to load Docker images</p>
            </div>
        `;
  }
}

/**
 * Filter images by type (all, pushed, proxy)
 * @param {string} filterType - Filter type: 'all', 'pushed', or 'proxy'
 */
function filterImages(filterType) {
  currentImageFilter = filterType;

  // Update button styles
  const buttons = document.querySelectorAll('.filter-btn');
  buttons.forEach(btn => {
    btn.classList.remove('active', 'bg-gray-700', 'text-white');
    // Reset to default colors based on button type
    if (btn.id === 'filterAll') {
      btn.classList.add('bg-gray-200', 'hover:bg-gray-300');
    }
  });

  // Highlight active button
  const activeBtn = document.getElementById(`filter${filterType.charAt(0).toUpperCase() + filterType.slice(1)}`);
  if (activeBtn) {
    activeBtn.classList.add('active');
    if (filterType === 'all') {
      activeBtn.classList.remove('bg-gray-200', 'hover:bg-gray-300');
      activeBtn.classList.add('bg-gray-700', 'text-white');
    } else if (filterType === 'pushed') {
      activeBtn.classList.add('bg-green-500', 'text-white');
      activeBtn.classList.remove('bg-green-100', 'text-green-800');
    } else if (filterType === 'proxy') {
      activeBtn.classList.add('bg-purple-500', 'text-white');
      activeBtn.classList.remove('bg-purple-100', 'text-purple-800');
    }
  }

  renderFilteredImages();
}

/**
 * Sort images by the selected criteria
 * @param {string} sortOrder - Sort order: 'date-desc', 'date-asc', 'size-desc', 'size-asc', 'name-asc', 'name-desc'
 */
function sortImages(sortOrder) {
  currentSortOrder = sortOrder;
  renderFilteredImages();
}

/**
 * Set the view mode (cards or list)
 * @param {string} mode - View mode: 'cards' or 'list'
 */
function setViewMode(mode) {
  currentViewMode = mode;

  // Update button styles
  const cardsBtn = document.getElementById('viewCards');
  const listBtn = document.getElementById('viewList');

  if (mode === 'cards') {
    cardsBtn.classList.add('active', 'bg-gray-700', 'text-white');
    cardsBtn.classList.remove('bg-gray-200', 'hover:bg-gray-300');
    listBtn.classList.remove('active', 'bg-gray-700', 'text-white');
    listBtn.classList.add('bg-gray-200', 'hover:bg-gray-300');
  } else {
    listBtn.classList.add('active', 'bg-gray-700', 'text-white');
    listBtn.classList.remove('bg-gray-200', 'hover:bg-gray-300');
    cardsBtn.classList.remove('active', 'bg-gray-700', 'text-white');
    cardsBtn.classList.add('bg-gray-200', 'hover:bg-gray-300');
  }

  renderFilteredImages();
}

/**
 * Deduplicate images by digest - group images with same digest together
 * IMPORTANT: Pushed and proxy images are kept separate even if they have the same digest
 * to avoid conflicts when deleting (different delete handlers are used)
 * @param {Array} images - Array of image metadata objects
 * @returns {Array} Deduplicated array with combined tags
 */
function deduplicateByDigest(images) {
  const digestMap = new Map();

  for (const img of images) {
    const digest = img.digest;
    const isPushed = img.isPushed === true;

    if (!digest) {
      // No digest - keep as separate entry with unique key
      digestMap.set(`no-digest-${img.name}-${img.tag}`, { ...img, allTags: [img.tag], allNames: [img.name] });
      continue;
    }

    // Use composite key: digest + source type (pushed vs proxy)
    // This prevents merging pushed and proxy images which use different delete handlers
    const key = `${digest}-${isPushed ? 'pushed' : 'proxy'}`;

    if (digestMap.has(key)) {
      const existing = digestMap.get(key);
      // Add tag if not already present
      const tagKey = `${img.name}:${img.tag}`;
      const existingTagKey = `${existing.name}:${existing.tag}`;
      if (tagKey !== existingTagKey) {
        if (!existing.allTags.includes(img.tag)) {
          existing.allTags.push(img.tag);
        }
        if (!existing.allNames.includes(img.name)) {
          existing.allNames.push(img.name);
        }
      }
      // Keep most recent date
      const existingDate = new Date(existing.lastAccessed || existing.cachedAt || existing.created || 0);
      const newDate = new Date(img.lastAccessed || img.cachedAt || img.created || 0);
      if (newDate > existingDate) {
        existing.lastAccessed = img.lastAccessed;
        existing.cachedAt = img.cachedAt;
        existing.created = img.created;
      }
    } else {
      digestMap.set(key, { ...img, allTags: [img.tag], allNames: [img.name] });
    }
  }

  return Array.from(digestMap.values());
}

/**
 * Render images based on current filter, sort order, and view mode
 */
function renderFilteredImages() {
  const container = document.getElementById("imagesContainer");
  const noImagesMessage = document.getElementById("noImagesMessage");
  const filterCountEl = document.getElementById("filterCount");

  // Apply filter
  let filteredImages = [...allDockerImages];
  if (currentImageFilter === 'pushed') {
    filteredImages = filteredImages.filter(img => img.isPushed === true);
  } else if (currentImageFilter === 'proxy') {
    filteredImages = filteredImages.filter(img => img.isPushed !== true);
  }

  // Deduplicate by digest
  filteredImages = deduplicateByDigest(filteredImages);

  // Apply sort
  filteredImages.sort((a, b) => {
    switch (currentSortOrder) {
      case 'date-desc':
        return new Date(b.lastAccessed || b.cachedAt || b.created || 0) - new Date(a.lastAccessed || a.cachedAt || a.created || 0);
      case 'date-asc':
        return new Date(a.lastAccessed || a.cachedAt || a.created || 0) - new Date(b.lastAccessed || b.cachedAt || b.created || 0);
      case 'size-desc':
        return (b.size || 0) - (a.size || 0);
      case 'size-asc':
        return (a.size || 0) - (b.size || 0);
      case 'name-asc':
        return (a.name || '').localeCompare(b.name || '');
      case 'name-desc':
        return (b.name || '').localeCompare(a.name || '');
      default:
        return 0;
    }
  });

  // Update filter count (show deduplicated count)
  const totalUnique = deduplicateByDigest(allDockerImages).length;
  const pushedCount = deduplicateByDigest(allDockerImages.filter(img => img.isPushed === true)).length;
  const proxyCount = deduplicateByDigest(allDockerImages.filter(img => img.isPushed !== true)).length;
  if (filterCountEl) {
    filterCountEl.textContent = `${filteredImages.length} unique (${pushedCount} pushed, ${proxyCount} proxy)`;
  }

  if (filteredImages.length === 0) {
    container.innerHTML = "";
    if (allDockerImages.length === 0) {
      noImagesMessage.style.display = "flex";
    } else {
      // Show "no matches" message when filter has no results
      container.innerHTML = `
        <div class="col-span-full text-center text-gray-500 py-8">
          <i class="material-icons text-4xl mb-2">filter_list_off</i>
          <p>No ${currentImageFilter === 'pushed' ? 'pushed' : 'proxy'} images found</p>
        </div>
      `;
      noImagesMessage.style.display = "none";
    }
    return;
  }

  noImagesMessage.style.display = "none";

  // Render based on view mode
  if (currentViewMode === 'list') {
    container.className = 'w-full';
    container.innerHTML = createImageListView(filteredImages);
  } else {
    container.className = 'grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-6';
    container.innerHTML = filteredImages
      .map((image) => createImageCard(image))
      .join("");
  }
}

/**
 * Create HTML card for a Docker image (both pushed and cached)
 * @param {Object} image - Image metadata object
 * @returns {string} HTML string for the image card
 */
function createImageCard(image) {
  const formatSize = (bytes) => {
    if (!bytes) return "Unknown";
    const sizes = ["B", "KB", "MB", "GB"];
    const i = Math.floor(Math.log(bytes) / Math.log(1024));
    return (bytes / Math.pow(1024, i)).toFixed(2) + " " + sizes[i];
  };

  const formatDate = (dateStr) => {
    if (!dateStr) return "Unknown";
    const date = new Date(dateStr);
    return date.toLocaleDateString() + " " + date.toLocaleTimeString();
  };

  const name = image.name;
  // Use tag from originalRef if available (more reliable than tag field)
  // originalRef format: "namespace/image:tag" or "image:tag"
  let tag = image.tag;
  if (image.originalRef && image.originalRef.includes(':')) {
    tag = image.originalRef.split(':').pop();
  }

  // Get all tags if deduplicated
  const allTags = image.allTags || [tag];
  const tagsDisplay = allTags.length > 1
    ? allTags.map(t => `<span class="inline-block bg-gray-100 px-1.5 py-0.5 rounded text-xs mr-1">${t}</span>`).join('')
    : `<span class="font-mono">${tag}</span>`;

  // Determine the source badge and color based on isPushed
  const isPushed = image.isPushed === true;
  const sourceLabel = isPushed ? "Pushed" : (image.sourceRegistry || "docker.io");
  const sourceColor = isPushed ? "bg-green-100 text-green-800" : "bg-purple-100 text-purple-800";
  const headerColor = isPushed ? "text-green-600" : "text-purple-600";
  const deleteHandler = isPushed
    ? `deleteImage('${name}', '${tag}')`
    : `deleteCachedImage('${name}', '${tag}')`;

  return `
        <div class="bg-white rounded-lg shadow-md p-6 flex flex-col h-[220px]" data-image-name="${name}" data-image-tag="${tag}">
            <div class="flex justify-between items-start mb-3">
                <div class="flex-1 min-w-0">
                    <div class="flex items-center gap-2 mb-1">
                        <span class="text-xs px-2 py-0.5 rounded ${sourceColor}">${sourceLabel}</span>
                        ${allTags.length > 1 ? `<span class="text-xs text-gray-500">${allTags.length} tags</span>` : ''}
                    </div>
                    <a href="/image/${name}/${encodeURIComponent(tag)}/details" class="hover:underline">
                        <h2 class="text-lg font-bold ${headerColor} truncate" title="${name}">
                            ${name}
                        </h2>
                    </a>
                    <p class="text-sm text-gray-600 mt-1">${allTags.length > 1 ? 'Tags: ' : 'Tag: '}${tagsDisplay}</p>
                </div>
                <div class="flex gap-2 ml-2">
                    <a href="#" onclick="${deleteHandler}; return false;" class="tooltip-trigger" data-tooltip="Delete image">
                        <i class="material-icons icon-delete text-red-500 hover:text-red-700">delete</i>
                    </a>
                </div>
            </div>
            <div class="flex-1 overflow-hidden text-sm text-gray-600">
                <p class="mb-1"><span class="font-semibold">Size:</span> ${formatSize(image.size)}</p>
                ${isPushed
                    ? `<p class="mb-1"><span class="font-semibold">Created:</span> ${formatDate(image.created)}</p>`
                    : `<p class="mb-1"><span class="font-semibold">Cached:</span> ${formatDate(image.cachedAt)}</p>
                       <p class="mb-1"><span class="font-semibold">Last Access:</span> ${formatDate(image.lastAccessed)}</p>`
                }
                <p class="text-xs text-gray-400 truncate" title="${image.digest || ''}">
                    <span class="font-semibold">Digest:</span> ${
                      image.digest
                        ? image.digest.replace('sha256:', '').substring(0, 12)
                        : "N/A"
                    }
                </p>
            </div>
        </div>
    `;
}

/**
 * Create HTML table view for Docker images
 * @param {Array} images - Array of image metadata objects
 * @returns {string} HTML string for the table view
 */
function createImageListView(images) {
  const formatSize = (bytes) => {
    if (!bytes) return "Unknown";
    const sizes = ["B", "KB", "MB", "GB"];
    const i = Math.floor(Math.log(bytes) / Math.log(1024));
    return (bytes / Math.pow(1024, i)).toFixed(2) + " " + sizes[i];
  };

  const formatDate = (dateStr) => {
    if (!dateStr) return "-";
    const date = new Date(dateStr);
    return date.toLocaleDateString() + " " + date.toLocaleTimeString();
  };

  const rows = images.map(image => {
    const name = image.name;
    let tag = image.tag;
    if (image.originalRef && image.originalRef.includes(':')) {
      tag = image.originalRef.split(':').pop();
    }

    // Get all tags if deduplicated
    const allTags = image.allTags || [tag];
    const allNames = image.allNames || [name];
    const tagsDisplay = allTags.length > 1
      ? allTags.map(t => `<span class="inline-block bg-gray-100 px-1.5 py-0.5 rounded text-xs mr-1 mb-1">${t}</span>`).join('')
      : `<span class="font-mono">${tag}</span>`;
    const namesDisplay = allNames.length > 1
      ? allNames.join(', ')
      : name;

    const isPushed = image.isPushed === true;
    const sourceLabel = isPushed ? "Pushed" : (image.sourceRegistry || "docker.io");
    const sourceColor = isPushed ? "bg-green-100 text-green-800" : "bg-purple-100 text-purple-800";
    const deleteHandler = isPushed
      ? `deleteImage('${name}', '${tag}')`
      : `deleteCachedImage('${name}', '${tag}')`;

    const dateValue = isPushed
      ? formatDate(image.created)
      : formatDate(image.lastAccessed || image.cachedAt);

    return `
      <tr class="border-b hover:bg-gray-50">
        <td class="py-3 px-4">
          <span class="text-xs px-2 py-0.5 rounded ${sourceColor}">${sourceLabel}</span>
        </td>
        <td class="py-3 px-4">
          <a href="/image/${name}/${encodeURIComponent(tag)}/details" class="text-blue-600 hover:underline font-medium">
            ${namesDisplay}
          </a>
        </td>
        <td class="py-3 px-4 text-sm">${tagsDisplay}</td>
        <td class="py-3 px-4 text-right">${formatSize(image.size)}</td>
        <td class="py-3 px-4 text-sm text-gray-600">${dateValue}</td>
        <td class="py-3 px-4 font-mono text-xs text-gray-400" title="${image.digest || ''}">
          ${image.digest ? image.digest.replace('sha256:', '').substring(0, 12) + '...' : '-'}
        </td>
        <td class="py-3 px-4 text-center">
          <a href="#" onclick="${deleteHandler}; return false;" class="text-red-500 hover:text-red-700">
            <i class="material-icons text-sm">delete</i>
          </a>
        </td>
      </tr>
    `;
  }).join('');

  return `
    <div class="bg-white rounded-lg shadow-md overflow-hidden">
      <table class="w-full">
        <thead class="bg-gray-50 border-b">
          <tr>
            <th class="py-3 px-4 text-left text-xs font-semibold text-gray-600 uppercase">Source</th>
            <th class="py-3 px-4 text-left text-xs font-semibold text-gray-600 uppercase">Name</th>
            <th class="py-3 px-4 text-left text-xs font-semibold text-gray-600 uppercase">Tag</th>
            <th class="py-3 px-4 text-right text-xs font-semibold text-gray-600 uppercase">Size</th>
            <th class="py-3 px-4 text-left text-xs font-semibold text-gray-600 uppercase">Date</th>
            <th class="py-3 px-4 text-left text-xs font-semibold text-gray-600 uppercase">Digest</th>
            <th class="py-3 px-4 text-center text-xs font-semibold text-gray-600 uppercase">Actions</th>
          </tr>
        </thead>
        <tbody>
          ${rows}
        </tbody>
      </table>
    </div>
  `;
}

/**
 * Create HTML card for a cached Docker image (from proxy cache)
 * @param {Object} image - CachedImageMetadata object
 * @returns {string} HTML string for the image card
 * @deprecated Use createImageCard instead
 */
function createCachedImageCard(image) {
  return createImageCard({ ...image, isPushed: false });
}

/**
 * Switch to a different tag for an image
 * @param {string} imageName - The image name
 * @param {string} tag - The tag to switch to
 */
function switchImageTag(imageName, tag) {
  const card = document.querySelector(`[data-image-name="${imageName}"]`);
  if (!card) return;

  // Update links
  const infoLink = card.querySelector(".icon-info").parentElement;
  const deleteLink = card.querySelector(".icon-delete").parentElement;

  infoLink.href = `/image/${imageName}/${tag}/details`;
  deleteLink.onclick = function () {
    deleteImage(imageName, tag);
  };
}

/**
 * Delete a Docker image tag
 * @param {string} name - The image name
 * @param {string} tag - The tag to delete
 */
async function deleteImage(name, tag) {
  if (!confirm(`Are you sure you want to delete ${name}:${tag}?`)) {
    return;
  }

  try {
    const response = await fetch(`/image/${name}/${tag}`, {
      method: "DELETE",
    });

    if (!response.ok) {
      const error = await response.json();
      throw new Error(error.error || "Failed to delete image");
    }

    showModal(`Image ${name}:${tag} deleted successfully`, false);
    loadDockerImages(); // Refresh the list
  } catch (error) {
    console.error("Error deleting image:", error);
    showModal(`Error deleting image: ${error.message}`);
  }
}

/**
 * Delete a cached Docker image from proxy cache
 * @param {string} name - The image name
 * @param {string} tag - The tag to delete
 */
async function deleteCachedImage(name, tag) {
  if (!confirm(`Are you sure you want to remove ${name}:${tag} from cache?`)) {
    return;
  }

  try {
    // Don't encode slashes in name - Fiber's :name+ route expects literal slashes
    // Only encode special characters that could break the URL
    const encodedName = name.split('/').map(segment => encodeURIComponent(segment)).join('/');
    const response = await fetch(`/cache/image/${encodedName}/${encodeURIComponent(tag)}`, {
      method: "DELETE",
    });

    if (!response.ok) {
      const error = await response.json();
      throw new Error(error.error || "Failed to delete cached image");
    }

    showModal(`Cached image ${name}:${tag} removed successfully`, false);
    loadDockerImages();
    loadCacheStatus();
  } catch (error) {
    console.error("Error deleting cached image:", error);
    showModal(`Error deleting cached image: ${error.message}`);
  }
}

// 🚀 Initialisation
document.addEventListener("DOMContentLoaded", function () {
  console.log("DOM loaded"); // Debug

  // Set portal hostname dynamically in all .portal-host spans
  const portalHost = window.location.host;
  document.querySelectorAll(".portal-host").forEach((el) => {
    el.textContent = portalHost;
  });

  // Default to images tab (proxy cache view)
  showTab("images");

  // Vérifier le statut de la fonctionnalité de backup
  checkBackupStatus();

  // Initialiser le gestionnaire d'événements pour le formulaire d'upload
  const uploadForm = document.getElementById("uploadForm");
  if (uploadForm) {
    uploadForm.addEventListener("submit", function () {
      const fileInput = this.querySelector('input[type="file"]');
      if (fileInput.files.length > 0) {
        fileInput.insertAdjacentHTML(
          "afterend",
          '<span class="ml-2 text-blue-600">Uploading ' +
            fileInput.files[0].name +
            "...</span>"
        );
      }
    });
  }

  // Sélectionner les boutons de fermeture de la modale par leur position plutôt que par l'attribut onclick
  const modalCloseIcon = document.querySelector("#errorModal .material-icons");
  const modalCloseButton = document.querySelector("#errorModal .bg-blue-600");

  if (modalCloseIcon) {
    modalCloseIcon.addEventListener("click", function () {
      closeErrorModal();
    });
  }

  if (modalCloseButton) {
    modalCloseButton.addEventListener("click", function () {
      closeErrorModal();
    });
  }

  // Remplacer le gestionnaire d'événement du bouton de backup
  const backupButton = document.getElementById("backupButton");
  if (backupButton) {
    // Supprimer l'attribut onclick pour éviter les conflits
    backupButton.removeAttribute("onclick");
    backupButton.addEventListener("click", function (e) {
      e.preventDefault();
      performBackup();
      return false;
    });
  }

  // Initialiser le cache des versions
  window.chartVersions = {};

  // Pré-charger les versions pour chaque chart
  const cards = document.querySelectorAll("[data-chart-name]");
  cards.forEach(async (card) => {
    const chartName = card.dataset.chartName;
    try {
      const versions = await fetchChartVersions(chartName);
      window.chartVersions[chartName] = versions;
    } catch (error) {
      console.error(`Error loading versions for ${chartName}:`, error);
    }
  });

  // Load pending scan badge count
  loadPendingBadge();
});

// =============================================
// Security Gate Functions
// =============================================

let currentScanFilter = "pending";
let allScanDecisions = [];
let scanConfirmCallback = null;

async function loadPendingBadge() {
  try {
    const resp = await fetch("/api/scan/summary");
    if (!resp.ok) return;
    const summary = await resp.json();
    const badge = document.getElementById("pendingBadge");
    if (badge && summary.pending > 0) {
      badge.textContent = summary.pending;
      badge.classList.remove("hidden");
    }
  } catch (e) {
    // Trivy not enabled, ignore
  }
}

async function loadScanSummary() {
  try {
    const resp = await fetch("/api/scan/summary");
    if (!resp.ok) return;
    const s = await resp.json();

    const el = (id, text) => { const e = document.getElementById(id); if (e) e.textContent = text; };
    el("summaryPending", s.pending + " Pending");
    el("summaryApproved", s.approved + " Approved");
    el("summaryDenied", s.denied + " Denied");
    el("summaryCritical", s.critical + " Critical");
    el("summaryHigh", s.high + " High");
  } catch (e) {
    console.error("Failed to load scan summary:", e);
  }
}

async function loadScanDecisions(filter) {
  currentScanFilter = filter;
  try {
    const url = filter === "pending" ? "/api/scan/pending" : "/api/scan/all";
    const resp = await fetch(url);
    if (!resp.ok) {
      document.getElementById("noScanResults").style.display = "flex";
      document.getElementById("scanDecisionsContainer").style.display = "none";
      return;
    }
    allScanDecisions = await resp.json();

    // Update filter button styles
    document.querySelectorAll(".scan-filter-btn").forEach(btn => {
      btn.classList.remove("bg-yellow-500", "text-white");
      btn.classList.add("bg-gray-200", "text-gray-700");
    });
    const activeBtn = filter === "pending" ? document.getElementById("scanFilterPending") : document.getElementById("scanFilterAll");
    if (activeBtn) {
      activeBtn.classList.remove("bg-gray-200", "text-gray-700");
      activeBtn.classList.add("bg-yellow-500", "text-white");
    }

    renderScanDecisions(allScanDecisions);
  } catch (e) {
    console.error("Failed to load scan decisions:", e);
  }
}

function filterScanDecisions(filter) {
  loadScanDecisions(filter);
}

function renderScanDecisions(decisions) {
  const tbody = document.getElementById("scanDecisionsBody");
  const noResults = document.getElementById("noScanResults");
  const container = document.getElementById("scanDecisionsContainer");

  if (!decisions || decisions.length === 0) {
    noResults.style.display = "flex";
    container.style.display = "none";
    return;
  }

  noResults.style.display = "none";
  container.style.display = "block";

  tbody.innerHTML = decisions.map(d => {
    const sr = d.scanResult || {};
    const statusColors = {
      pending: "bg-yellow-100 text-yellow-800",
      approved: "bg-green-100 text-green-800",
      denied: "bg-red-100 text-red-800"
    };
    const statusClass = statusColors[d.status] || "bg-gray-100 text-gray-800";
    const digestFull = d.digest ? d.digest.replace("sha256:", "") : "";
    const digestDisplay = digestFull.substring(0, 12);
    const date = d.decidedAt ? new Date(d.decidedAt).toLocaleDateString() : "-";

    let cveHtml = "";
    if (sr.critical > 0) cveHtml += `<span class="bg-red-600 text-white text-xs px-1.5 py-0.5 rounded">${sr.critical}C</span> `;
    if (sr.high > 0) cveHtml += `<span class="bg-orange-500 text-white text-xs px-1.5 py-0.5 rounded">${sr.high}H</span> `;
    if (sr.medium > 0) cveHtml += `<span class="bg-yellow-500 text-white text-xs px-1.5 py-0.5 rounded">${sr.medium}M</span> `;
    if (sr.low > 0) cveHtml += `<span class="bg-blue-400 text-white text-xs px-1.5 py-0.5 rounded">${sr.low}L</span>`;
    if (!cveHtml) cveHtml = '<span class="text-gray-400 text-xs">No scan</span>';

    let actionsHtml = "";
    if (d.status === "pending") {
      actionsHtml = `
        <button onclick="showApproveModal('${digestFull}')" class="bg-green-500 text-white px-2 py-1 rounded text-xs hover:bg-green-600" title="Approve">
          <i class="material-icons text-sm">check_circle</i>
        </button>
        <button onclick="showDenyModal('${digestFull}')" class="bg-red-500 text-white px-2 py-1 rounded text-xs hover:bg-red-600" title="Deny">
          <i class="material-icons text-sm">block</i>
        </button>
      `;
    } else {
      actionsHtml = `
        <button onclick="resetDecision('${digestFull}')" class="bg-gray-400 text-white px-2 py-1 rounded text-xs hover:bg-gray-500" title="Reset decision">
          <i class="material-icons text-sm">refresh</i>
        </button>
      `;
    }
    actionsHtml += `
      <button onclick="viewScanReport('${digestFull}')" class="bg-blue-500 text-white px-2 py-1 rounded text-xs hover:bg-blue-600" title="View report">
        <i class="material-icons text-sm">description</i>
      </button>
    `;

    return `<tr class="hover:bg-gray-50">
      <td class="px-4 py-3 text-sm font-medium text-gray-900">${d.imageName || "-"}</td>
      <td class="px-4 py-3 text-sm text-gray-600">${d.tag || "-"}</td>
      <td class="px-4 py-3">${cveHtml}</td>
      <td class="px-4 py-3"><span class="px-2 py-1 rounded-full text-xs font-medium ${statusClass}">${d.status}</span></td>
      <td class="px-4 py-3 text-sm text-gray-600">${d.decidedBy || "-"}</td>
      <td class="px-4 py-3 text-sm text-gray-600">${date}</td>
      <td class="px-4 py-3"><div class="flex gap-1">${actionsHtml}</div></td>
    </tr>`;
  }).join("");
}

function showApproveModal(digestShort) {
  const modal = document.getElementById("scanConfirmModal");
  const title = document.getElementById("scanConfirmTitle");
  const btn = document.getElementById("scanConfirmBtn");
  const expiryDiv = document.getElementById("scanConfirmExpiry");

  title.textContent = "Approve Image";
  btn.textContent = "Approve";
  btn.className = "px-4 py-2 rounded text-white text-sm bg-green-600 hover:bg-green-700";
  expiryDiv.classList.remove("hidden");
  document.getElementById("scanConfirmReason").value = "";
  document.getElementById("scanConfirmExpiryDays").value = "0";

  scanConfirmCallback = () => approveImage(digestShort);
  modal.style.display = "flex";
  modal.classList.remove("hidden");
}

function showDenyModal(digestShort) {
  const modal = document.getElementById("scanConfirmModal");
  const title = document.getElementById("scanConfirmTitle");
  const btn = document.getElementById("scanConfirmBtn");
  const expiryDiv = document.getElementById("scanConfirmExpiry");

  title.textContent = "Deny Image";
  btn.textContent = "Deny";
  btn.className = "px-4 py-2 rounded text-white text-sm bg-red-600 hover:bg-red-700";
  expiryDiv.classList.add("hidden");
  document.getElementById("scanConfirmReason").value = "";

  scanConfirmCallback = () => denyImage(digestShort);
  modal.style.display = "flex";
  modal.classList.remove("hidden");
}

function closeScanConfirmModal() {
  const modal = document.getElementById("scanConfirmModal");
  modal.style.display = "none";
  modal.classList.add("hidden");
  scanConfirmCallback = null;
}

// Wire up confirm button
document.addEventListener("DOMContentLoaded", () => {
  const confirmBtn = document.getElementById("scanConfirmBtn");
  if (confirmBtn) {
    confirmBtn.addEventListener("click", () => {
      if (scanConfirmCallback) scanConfirmCallback();
    });
  }
});

async function approveImage(digestShort) {
  const reason = document.getElementById("scanConfirmReason").value || "Approved by admin";
  const expiresInDays = parseInt(document.getElementById("scanConfirmExpiryDays").value) || 0;

  try {
    const resp = await fetch(`/api/scan/approve/${digestShort}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ reason, decidedBy: "admin", expiresInDays })
    });
    if (resp.ok) {
      closeScanConfirmModal();
      loadScanDecisions(currentScanFilter);
      loadScanSummary();
      loadPendingBadge();
      showModal("Image approved successfully", false);
    } else {
      showModal("Failed to approve image");
    }
  } catch (e) {
    showModal("Error approving image: " + e.message);
  }
}

async function denyImage(digestShort) {
  const reason = document.getElementById("scanConfirmReason").value || "Denied by admin";

  try {
    const resp = await fetch(`/api/scan/deny/${digestShort}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ reason, decidedBy: "admin" })
    });
    if (resp.ok) {
      closeScanConfirmModal();
      loadScanDecisions(currentScanFilter);
      loadScanSummary();
      loadPendingBadge();
      showModal("Image denied", false);
    } else {
      showModal("Failed to deny image");
    }
  } catch (e) {
    showModal("Error denying image: " + e.message);
  }
}

async function resetDecision(digestShort) {
  if (!confirm("Reset this decision? The image will require re-review.")) return;

  try {
    const resp = await fetch(`/api/scan/decision/${digestShort}`, { method: "DELETE" });
    if (resp.ok) {
      loadScanDecisions(currentScanFilter);
      loadScanSummary();
      loadPendingBadge();
    } else {
      showModal("Failed to reset decision");
    }
  } catch (e) {
    showModal("Error resetting decision: " + e.message);
  }
}

async function viewScanReport(digestShort) {
  try {
    console.log("viewScanReport called with digest:", digestShort, "length:", digestShort.length);
    const resp = await fetch(`/api/scan/report/${digestShort}`);
    console.log("API response status:", resp.status);
    if (!resp.ok) {
      showModal("Scan report not found");
      return;
    }
    const data = await resp.json();
    const sr = data.scanResult;
    const decision = data.decision;

    const modal = document.getElementById("scanDetailModal");
    const content = document.getElementById("scanDetailContent");
    const actions = document.getElementById("scanDetailActions");

    let html = `
      <div class="mb-4">
        <p class="text-sm text-gray-600">Image: <strong>${sr.imageName}:${sr.tag}</strong></p>
        <p class="text-sm text-gray-600">Digest: <code class="bg-gray-100 px-1 rounded text-xs">${sr.digest}</code></p>
        <p class="text-sm text-gray-600">Scanned: ${new Date(sr.scannedAt).toLocaleString()}</p>
      </div>
      <div class="flex gap-3 mb-4">
        <span class="bg-red-600 text-white px-3 py-1 rounded font-bold">${sr.critical} Critical</span>
        <span class="bg-orange-500 text-white px-3 py-1 rounded font-bold">${sr.high} High</span>
        <span class="bg-yellow-500 text-white px-3 py-1 rounded font-bold">${sr.medium} Medium</span>
        <span class="bg-blue-400 text-white px-3 py-1 rounded font-bold">${sr.low} Low</span>
      </div>
    `;

    if (sr.vulnerabilities && sr.vulnerabilities.length > 0) {
      html += `<div class="max-h-96 overflow-y-auto">
        <table class="w-full text-sm">
          <thead class="bg-gray-50 sticky top-0">
            <tr>
              <th class="px-2 py-1 text-left">CVE</th>
              <th class="px-2 py-1 text-left">Severity</th>
              <th class="px-2 py-1 text-left">Package</th>
              <th class="px-2 py-1 text-left">Version</th>
              <th class="px-2 py-1 text-left">Fixed In</th>
            </tr>
          </thead>
          <tbody class="divide-y">`;

      sr.vulnerabilities.forEach(v => {
        const sevColors = {
          CRITICAL: "bg-red-100 text-red-800",
          HIGH: "bg-orange-100 text-orange-800",
          MEDIUM: "bg-yellow-100 text-yellow-800",
          LOW: "bg-blue-100 text-blue-800"
        };
        const sevClass = sevColors[v.severity.toUpperCase()] || "bg-gray-100";
        const link = v.link ? `<a href="${v.link}" target="_blank" class="text-blue-600 hover:underline">${v.id}</a>` : v.id;
        html += `<tr class="hover:bg-gray-50">
          <td class="px-2 py-1">${link}</td>
          <td class="px-2 py-1"><span class="px-1.5 py-0.5 rounded text-xs ${sevClass}">${v.severity}</span></td>
          <td class="px-2 py-1">${v.package}</td>
          <td class="px-2 py-1 text-xs">${v.version}</td>
          <td class="px-2 py-1 text-xs">${v.fixedIn || "-"}</td>
        </tr>`;
      });

      html += "</tbody></table></div>";
    }

    content.innerHTML = html;

    // Actions
    if (decision && decision.status === "pending") {
      actions.innerHTML = `
        <button onclick="closeScanModal(); showApproveModal('${digestShort}')" class="bg-green-600 text-white px-4 py-2 rounded text-sm hover:bg-green-700">Approve</button>
        <button onclick="closeScanModal(); showDenyModal('${digestShort}')" class="bg-red-600 text-white px-4 py-2 rounded text-sm hover:bg-red-700">Deny</button>
      `;
    } else {
      actions.innerHTML = `<button onclick="closeScanModal()" class="bg-gray-200 px-4 py-2 rounded text-sm hover:bg-gray-300">Close</button>`;
    }

    modal.style.display = "flex";
    modal.classList.remove("hidden");
  } catch (e) {
    showModal("Error loading scan report: " + e.message);
  }
}

function closeScanModal() {
  const modal = document.getElementById("scanDetailModal");
  modal.style.display = "none";
  modal.classList.add("hidden");
}
