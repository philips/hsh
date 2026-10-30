#!/bin/bash
set -e

cd "$(dirname "$0")"

echo "Starting hsh website development server..."
echo "Open http://localhost:3000 in your browser"
echo ""

npm run dev
