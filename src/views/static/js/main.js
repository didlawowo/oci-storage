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
 * Current active tab
 */
let activeTab = "charts";

/**
 * Switch between charts and images tabs
 * @param {string} tab - The tab to show ('charts' or 'images')
 */
function showTab(tab) {
  activeTab = tab;

  const chartsSection = document.getElementById("chartsSection");
  const imagesSection = document.getElementById("imagesSection");
  const chartsTab = document.getElementById("chartsTab");
  const imagesTab = document.getElementById("imagesTab");

  if (tab === "charts") {
    chartsSection.style.display = "block";
    imagesSection.style.display = "none";
    chartsTab.classList.add("active", "bg-blue-700");
    imagesTab.classList.remove("active", "bg-blue-700");
  } else {
    chartsSection.style.display = "none";
    imagesSection.style.display = "block";
    chartsTab.classList.remove("active", "bg-blue-700");
    imagesTab.classList.add("active", "bg-blue-700");
    // Load images and cache status when switching to images tab
    loadDockerImages();
    loadCacheStatus();
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

/**
 * Fetch and display Docker images
 */
async function loadDockerImages() {
  const container = document.getElementById("imagesContainer");
  const noImagesMessage = document.getElementById("noImagesMessage");

  try {
    const response = await fetch("/images");
    const data = await response.json();

    if (!data.images || data.images.length === 0) {
      container.innerHTML = "";
      noImagesMessage.style.display = "flex";
      return;
    }

    noImagesMessage.style.display = "none";
    container.innerHTML = data.images
      .map((imageGroup) => createImageCard(imageGroup))
      .join("");
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
 * Create HTML card for a Docker image
 * @param {Object} imageGroup - The image group data
 * @returns {string} HTML string for the image card
 */
function createImageCard(imageGroup) {
  const name = imageGroup.name;
  const tags = imageGroup.tags || [];
  const firstTag = tags.length > 0 ? tags[0] : null;

  if (!firstTag) {
    return "";
  }

  // Format size
  const formatSize = (bytes) => {
    if (!bytes) return "Unknown";
    const sizes = ["B", "KB", "MB", "GB"];
    const i = Math.floor(Math.log(bytes) / Math.log(1024));
    return (bytes / Math.pow(1024, i)).toFixed(2) + " " + sizes[i];
  };

  const tagsHtml =
    tags.length > 1
      ? `<select class="mt-2 text-sm border rounded p-1" onchange="switchImageTag('${name}', this.value)">
             ${tags
               .map((t) => `<option value="${t.tag}">${t.tag}</option>`)
               .join("")}
           </select>`
      : `<p class="mt-2 text-sm text-gray-600">Tag: ${firstTag.tag}</p>`;

  return `
        <div class="bg-white rounded-lg shadow-md p-6 flex flex-col h-[200px]" data-image-name="${name}">
            <div class="flex justify-between items-start mb-4">
                <div>
                    <h2 class="text-lg font-bold text-purple-600">
                        <a href="/image/${name}/${
    firstTag.tag
  }/details">${name}</a>
                    </h2>
                    ${tagsHtml}
                </div>
                <div class="flex gap-2">
                    <a href="/image/${name}/${
    firstTag.tag
  }/details" class="tooltip-trigger" data-tooltip="View image details">
                        <i class="material-icons icon-info text-blue-500 hover:text-blue-700">info</i>
                    </a>
                    <a href="#" onclick="deleteImage('${name}', '${
    firstTag.tag
  }')" class="tooltip-trigger" data-tooltip="Delete this tag">
                        <i class="material-icons icon-delete text-red-500 hover:text-red-700">delete</i>
                    </a>
                </div>
            </div>
            <div class="flex-1 overflow-hidden">
                <div class="text-sm text-gray-600 mb-2">
                    <p><span class="font-semibold">Size:</span> ${formatSize(
                      firstTag.size
                    )}</p>
                    <p><span class="font-semibold">Layers:</span> ${
                      firstTag.layers ? firstTag.layers.length : "N/A"
                    }</p>
                </div>
                <p class="text-gray-500 text-xs truncate">
                    <span class="font-semibold">Digest:</span> ${
                      firstTag.digest
                        ? firstTag.digest.substring(0, 20) + "..."
                        : "N/A"
                    }
                </p>
            </div>
        </div>
    `;
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

// 🚀 Initialisation
document.addEventListener("DOMContentLoaded", function () {
  console.log("DOM loaded"); // Debug

  // Set portal hostname dynamically in all .portal-host spans
  const portalHost = window.location.host;
  document.querySelectorAll(".portal-host").forEach((el) => {
    el.textContent = portalHost;
  });

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
});
