// @ts-check

/** @type {import('@docusaurus/plugin-content-docs').SidebarsConfig} */
const sidebars = {
  guide: [
    "index",
    "screenshots",
    {
      type: "category",
      label: "Guide",
      collapsible: false,
      items: [
        "requirements",
        "getting-started",
        "without-docker",
        "sites",
        "accounts",
        "api-keys",
      ],
    },
    {
      type: "category",
      label: "Reference",
      collapsible: false,
      items: ["configuration", "security", "hardened", "images", "development"],
    },
  ],
};

module.exports = sidebars;
