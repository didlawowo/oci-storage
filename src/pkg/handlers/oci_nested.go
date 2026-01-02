package handlers

import "github.com/gofiber/fiber/v2"

// Nested path handlers - combine namespace/name into single name parameter
// These allow paths like /v2/charts/myapp/... or /v2/images/myapp/...

func (h *OCIHandler) getNestedName(c *fiber.Ctx) string {
	namespace := c.Params("namespace")
	name := h.getName(c)
	return namespace + "/" + name
}

// getName returns the repository name, checking Locals first (for nested paths) then Params
func (h *OCIHandler) getName(c *fiber.Ctx) string {
	if name := c.Locals("name"); name != nil {
		return name.(string)
	}
	return c.Params("name")
}

// 2-segment nested handlers (e.g., charts/myapp)

func (h *OCIHandler) HandleListTagsNested(c *fiber.Ctx) error {
	c.Locals("name", h.getNestedName(c))
	return h.HandleListTags(c)
}

func (h *OCIHandler) HandleManifestNested(c *fiber.Ctx) error {
	c.Locals("name", h.getNestedName(c))
	return h.HandleManifest(c)
}

func (h *OCIHandler) PutManifestNested(c *fiber.Ctx) error {
	c.Locals("name", h.getNestedName(c))
	return h.PutManifest(c)
}

func (h *OCIHandler) PutBlobNested(c *fiber.Ctx) error {
	c.Locals("name", h.getNestedName(c))
	return h.PutBlob(c)
}

func (h *OCIHandler) PostUploadNested(c *fiber.Ctx) error {
	c.Locals("name", h.getNestedName(c))
	return h.PostUpload(c)
}

func (h *OCIHandler) PatchBlobNested(c *fiber.Ctx) error {
	c.Locals("name", h.getNestedName(c))
	return h.PatchBlob(c)
}

func (h *OCIHandler) CompleteUploadNested(c *fiber.Ctx) error {
	c.Locals("name", h.getNestedName(c))
	return h.CompleteUpload(c)
}

func (h *OCIHandler) HeadBlobNested(c *fiber.Ctx) error {
	c.Locals("name", h.getNestedName(c))
	return h.HeadBlob(c)
}

func (h *OCIHandler) GetBlobNested(c *fiber.Ctx) error {
	c.Locals("name", h.getNestedName(c))
	return h.GetBlob(c)
}

// 3-segment nested handlers (e.g., proxy/docker.io/nginx)

func (h *OCIHandler) getDeepNestedName(c *fiber.Ctx) string {
	ns1 := c.Params("ns1")
	ns2 := c.Params("ns2")
	name := c.Params("name")
	return ns1 + "/" + ns2 + "/" + name
}

func (h *OCIHandler) HandleListTagsDeepNested(c *fiber.Ctx) error {
	c.Locals("name", h.getDeepNestedName(c))
	return h.HandleListTags(c)
}

func (h *OCIHandler) HandleManifestDeepNested(c *fiber.Ctx) error {
	c.Locals("name", h.getDeepNestedName(c))
	return h.HandleManifest(c)
}

func (h *OCIHandler) HeadBlobDeepNested(c *fiber.Ctx) error {
	c.Locals("name", h.getDeepNestedName(c))
	return h.HeadBlob(c)
}

func (h *OCIHandler) GetBlobDeepNested(c *fiber.Ctx) error {
	c.Locals("name", h.getDeepNestedName(c))
	return h.GetBlob(c)
}

// 4-segment nested handlers (e.g., proxy/docker.io/library/nginx)

func (h *OCIHandler) getDeepNestedName4(c *fiber.Ctx) string {
	ns1 := c.Params("ns1")
	ns2 := c.Params("ns2")
	ns3 := c.Params("ns3")
	name := c.Params("name")
	return ns1 + "/" + ns2 + "/" + ns3 + "/" + name
}

func (h *OCIHandler) HandleListTagsDeepNested4(c *fiber.Ctx) error {
	c.Locals("name", h.getDeepNestedName4(c))
	return h.HandleListTags(c)
}

func (h *OCIHandler) HandleManifestDeepNested4(c *fiber.Ctx) error {
	c.Locals("name", h.getDeepNestedName4(c))
	return h.HandleManifest(c)
}

func (h *OCIHandler) HeadBlobDeepNested4(c *fiber.Ctx) error {
	c.Locals("name", h.getDeepNestedName4(c))
	return h.HeadBlob(c)
}

func (h *OCIHandler) GetBlobDeepNested4(c *fiber.Ctx) error {
	c.Locals("name", h.getDeepNestedName4(c))
	return h.GetBlob(c)
}

// 5-segment nested handlers (e.g., proxy/ghcr.io/actions/gha-runner-scale-set-controller)

func (h *OCIHandler) getDeepNestedName5(c *fiber.Ctx) string {
	ns1 := c.Params("ns1")
	ns2 := c.Params("ns2")
	ns3 := c.Params("ns3")
	ns4 := c.Params("ns4")
	name := c.Params("name")
	return ns1 + "/" + ns2 + "/" + ns3 + "/" + ns4 + "/" + name
}

func (h *OCIHandler) HandleListTagsDeepNested5(c *fiber.Ctx) error {
	c.Locals("name", h.getDeepNestedName5(c))
	return h.HandleListTags(c)
}

func (h *OCIHandler) HandleManifestDeepNested5(c *fiber.Ctx) error {
	c.Locals("name", h.getDeepNestedName5(c))
	return h.HandleManifest(c)
}

func (h *OCIHandler) HeadBlobDeepNested5(c *fiber.Ctx) error {
	c.Locals("name", h.getDeepNestedName5(c))
	return h.HeadBlob(c)
}

func (h *OCIHandler) GetBlobDeepNested5(c *fiber.Ctx) error {
	c.Locals("name", h.getDeepNestedName5(c))
	return h.GetBlob(c)
}
