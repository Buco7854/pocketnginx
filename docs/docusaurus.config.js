// @ts-check
const { themes } = require("prism-react-renderer");

// GitHub project pages live under /<repo>/. For a custom domain set
// DOCS_BASE_URL="/" (and add docs/static/CNAME with the domain); after a
// repo rename set it to "/<new-repo>/".
const baseUrl = process.env.DOCS_BASE_URL || "/lightngx/";
const url = process.env.DOCS_URL || "https://buco7854.github.io";

/** @type {import('@docusaurus/types').Config} */
const config = {
  title: "Lightngx",
  tagline: "A lightweight web UI for managing nginx.",
  favicon: "img/favicon.svg",

  url,
  baseUrl,
  organizationName: "buco7854",
  projectName: "lightngx",

  onBrokenLinks: "throw",
  markdown: { hooks: { onBrokenMarkdownLinks: "warn" } },

  headTags: [
    {
      tagName: "link",
      attributes: { rel: "apple-touch-icon", sizes: "180x180", href: `${baseUrl}img/apple-touch-icon.png` },
    },
  ],

  i18n: { defaultLocale: "en", locales: ["en"] },

  presets: [
    [
      "classic",
      /** @type {import('@docusaurus/preset-classic').Options} */
      ({
        docs: {
          routeBasePath: "/",
          sidebarPath: require.resolve("./sidebars.js"),
          editUrl: "https://github.com/buco7854/lightngx/tree/main/docs/",
        },
        blog: false,
        theme: {
          customCss: require.resolve("./src/css/custom.css"),
        },
      }),
    ],
  ],

  themeConfig:
    /** @type {import('@docusaurus/preset-classic').ThemeConfig} */
    ({
      image: "img/social-card.png",
      metadata: [
        { property: "og:site_name", content: "Lightngx" },
        { property: "og:type", content: "website" },
        { name: "theme-color", content: "#009639" },
        { name: "twitter:description", content: "A lightweight web UI for managing nginx." },
        { name: "twitter:image:alt", content: "Lightngx, a lightweight web UI for managing nginx" },
      ],
      colorMode: { respectPrefersColorScheme: true },
      navbar: {
        title: "Lightngx",
        logo: { alt: "Lightngx", src: "img/favicon.svg" },
        items: [
          { to: "/getting-started", label: "Getting started", position: "left" },
          { to: "/configuration", label: "Configuration", position: "left" },
          {
            href: "https://github.com/buco7854/lightngx",
            label: "GitHub",
            position: "right",
          },
        ],
      },
      footer: {
        style: "dark",
        links: [
          {
            title: "Docs",
            items: [
              { label: "Getting started", to: "/getting-started" },
              { label: "Configuration", to: "/configuration" },
              { label: "Security", to: "/security" },
            ],
          },
          {
            title: "Project",
            items: [
              { label: "GitHub", href: "https://github.com/buco7854/lightngx" },
              {
                label: "Container images",
                href: "https://github.com/buco7854/lightngx/pkgs/container/lightngx",
              },
            ],
          },
        ],
        copyright: "Built by Buco7854.",
      },
      prism: {
        theme: themes.oneLight,
        darkTheme: themes.oneDark,
        additionalLanguages: ["nginx", "bash"],
      },
    }),
};

module.exports = config;
