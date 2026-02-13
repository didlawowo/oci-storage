package handlers

import (
	"fmt"
	"io"
	"oci-storage/pkg/interfaces"
	utils "oci-storage/pkg/utils"
	"oci-storage/pkg/version"
	"os"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/sirupsen/logrus"
)

type HelmHandler struct {
	service     interfaces.ChartServiceInterface
	log         *utils.Logger
	pathManager *utils.PathManager
}

type IndexHandler struct {
	service     interfaces.ChartServiceInterface
	log         *utils.Logger
	pathManager *utils.PathManager
}

func NewHelmHandler(service interfaces.ChartServiceInterface, pathManager *utils.PathManager, logger *utils.Logger) *HelmHandler {
	return &HelmHandler{
		service:     service,
		log:         logger,
		pathManager: pathManager,
	}
}

func NewIndexHandler(service interfaces.ChartServiceInterface, pathManager *utils.PathManager, logger *utils.Logger) *IndexHandler {
	return &IndexHandler{
		service:     service,
		log:         logger,
		pathManager: pathManager,
	}
}

func (h *HelmHandler) GetChartVersions(c *fiber.Ctx) error {
	name := c.Params("name")
	h.log.WithFunc().WithField("chart", name).Debug("Fetching chart versions")

	chartGroups, err := h.service.ListCharts()
	if err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to fetch chart versions")
		return HTTPError(c, 500, "Failed to fetch chart versions")
	}

	h.log.WithFunc().WithField("chartGroupsCount", len(chartGroups)).Debug("Chart groups count")

	foundChart := false
	for _, group := range chartGroups {
		h.log.WithFunc().WithFields(logrus.Fields{
			"groupName":     group.Name,
			"requestedName": name,
			"versionsCount": len(group.Versions),
		}).Debug("Checking chart group")

		if group.Name == name {
			foundChart = true
			if len(group.Versions) == 0 {
				h.log.WithFunc().Debug("Chart found but no versions available")
				return HTTPError(c, 404, "No versions found for this chart")
			}
			return c.JSON(group.Versions)
		}
	}

	if !foundChart {
		h.log.WithFunc().WithField("chart", name).Debug("Chart not found")
		return HTTPError(c, 404, "Chart not found")
	}

	// Ne devrait jamais arriver ici mais par sécurité
	return HTTPError(c, 500, "Unknown error occurred")
}

func (h *IndexHandler) GetIndex(c *fiber.Ctx) error {
	indexPath := h.pathManager.GetIndexPath()
	h.log.WithFunc().WithField("path", indexPath).Debug("Processing index.yaml request")

	// Read file directly instead of SendFile to avoid fasthttp file caching
	// which can serve stale index.yaml after chart upload/delete
	data, err := os.ReadFile(indexPath)
	if err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to read index.yaml")
		return HTTPError(c, 500, "Failed to read index")
	}
	c.Set("Content-Type", "text/yaml; charset=utf-8")
	return c.Send(data)
}

func (h *HelmHandler) GetChart(c *fiber.Ctx) error {
	chartName := c.Params("name")
	version := c.Params("version")

	h.log.WithFunc().WithFields(logrus.Fields{
		"chart":   chartName,
		"version": version,
	}).Debug("Processing chart request")

	if !h.service.ChartExists(chartName, version) {
		h.log.WithFunc().WithFields(logrus.Fields{
			"chart":   chartName,
			"version": version,
		}).Debug("Chart not found")
		return HTTPError(c, 404, "Chart not found")
	}

	return c.SendFile(h.pathManager.GetChartPath(chartName, version))
}

func (h *HelmHandler) ListCharts(c *fiber.Ctx) error {
	h.log.WithFunc().Debug("Listing all charts")

	charts, err := h.service.ListCharts()
	if err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to list charts")
		return HTTPError(c, 500, "Failed to list charts")
	}
	return c.JSON(charts)
}

func (h *HelmHandler) UploadChart(c *fiber.Ctx) error {
	h.log.WithFunc().Debug("Processing chart upload")

	file, err := c.FormFile("chart")
	if err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to get uploaded file")
		return HTTPError(c, 400, "No chart file provided")
	}

	if !strings.HasSuffix(file.Filename, ".tgz") {
		h.log.WithFunc().WithField("filename", file.Filename).Error("Invalid file type")
		return HTTPError(c, 400, "Chart must be a .tgz file")
	}

	fileContent, err := file.Open()
	if err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to open uploaded file")
		return HTTPError(c, 500, "Failed to process file")
	}
	defer fileContent.Close()

	chartData, err := io.ReadAll(fileContent)
	if err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to read file content")
		return HTTPError(c, 500, "Failed to read file")
	}

	if err := h.service.SaveChart(chartData, file.Filename); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to save chart")
		return HTTPError(c, 500, "Failed to save chart")
	}

	h.log.WithFunc().WithField("filename", file.Filename).Info("Chart uploaded successfully")
	return c.Redirect("/", fiber.StatusSeeOther)
}

func (h *HelmHandler) DownloadChart(c *fiber.Ctx) error {
	name := c.Params("name")
	version := c.Params("version")

	h.log.WithFunc().WithFields(logrus.Fields{
		"name":    name,
		"version": version,
	}).Debug("Processing chart download")

	chart, err := h.service.GetChart(name, version)
	if err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to get chart")
		return HTTPError(c, 500, "Failed to get chart")
	}

	fileName := fmt.Sprintf("%s-%s.tgz", name, version)
	c.Set("Content-Type", "application/gzip")
	c.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fileName))
	c.Set("Content-Length", fmt.Sprintf("%d", len(chart)))

	return c.Send(chart)
}

func (h *HelmHandler) DeleteChart(c *fiber.Ctx) error {
	name := c.Params("name")
	version := c.Params("version")

	h.log.WithFunc().WithFields(logrus.Fields{
		"name":    name,
		"version": version,
	}).Debug("Processing chart deletion")

	if err := h.service.DeleteChart(name, version); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to delete chart")
		return HTTPError(c, 500, "Failed to delete chart")
	}

	h.log.WithFunc().WithFields(logrus.Fields{
		"name":    name,
		"version": version,
	}).Info("Chart deleted successfully")
	return c.JSON(fiber.Map{"message": "Chart deleted successfully"})
}

func (h *HelmHandler) DisplayHome(c *fiber.Ctx) error {
	h.log.WithFunc().Debug("Processing home page request")

	chartGroups, err := h.service.ListCharts()
	if err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to list charts")
		return c.Status(500).Render("error", fiber.Map{
			"Error": "Failed to load charts",
		})
	}

	return c.Render("home", fiber.Map{
		"Charts":  chartGroups,
		"Title":   "OCI Storage",
		"Version": version.String(),
	})
}

func (h *HelmHandler) DisplayChartDetails(c *fiber.Ctx) error {
	name := c.Params("name")
	version := c.Params("version")

	h.log.WithFunc().WithFields(logrus.Fields{
		"name":    name,
		"version": version,
	}).Debug("Processing chart details request")

	chart, err := h.service.GetChartDetails(name, version)
	if err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to get chart details")
		return c.Status(500).Render("error", fiber.Map{
			"Error": "Failed to load chart",
		})
	}

	valuesContent, err := h.service.GetChartValues(name, version)
	if err != nil {
		h.log.WithFunc().WithError(err).Debug("Values.yaml not found")
		valuesContent = "No values.yaml found"
	}

	// Get all available versions for this chart
	versions, err := h.service.ListChartVersions(name)
	if err != nil {
		h.log.WithFunc().WithError(err).Debug("Failed to list chart versions")
		versions = []string{version}
	}

	chartDetails := fiber.Map{
		"Name":         chart.Name,
		"Version":      chart.Version,
		"AppVersion":   chart.AppVersion,
		"Description":  chart.Description,
		"Type":         chart.Type,
		"Dependencies": chart.Dependencies,
		"Values":       valuesContent,
	}

	return c.Render("details", fiber.Map{
		"Chart":    chartDetails,
		"Title":    "Chart Details - " + chart.Name,
		"Versions": versions,
	})
}
