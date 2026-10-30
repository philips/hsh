# hsh Website Setup for GitHub Pages

## Overview

Static website for hsh built with Astro, deployed to GitHub Pages at https://hsh.ifup.org.

## Content

- hsh logo
- Explainer paragraph and "exam proctor" analogy from the top of the README
- Install one-liner: `curl -fsSL https://raw.githubusercontent.com/philips/hsh/main/install.sh | sh`
- Link to the README on GitHub

## Styling

- Black/white color scheme
- Links in color #6AC4A2 (R:0x6A G:0xC4 B:0xA2)
- CSS inspired by ifup.org

## Deployment

The site deploys automatically on pushes to main via `.github/workflows/pages.yml`.

To set up the custom domain:
1. Create a CNAME record for `hsh.ifup.org` pointing to `philips.github.io`
2. In GitHub repository Settings > Pages, set source to "GitHub Actions"

## Local Development

```bash
cd astro-site
npm install
npm run dev
```

Open http://localhost:4321 in your browser.

Note: `astro preview` (static output) does not support `vite.preview.allowedHosts`;
use `astro dev` for access from external hostnames. The Tailscale hostname is
allowlisted via `vite.server.allowedHosts` in `astro.config.mjs`.
