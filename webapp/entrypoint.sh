#!/bin/bash
set -euo pipefail

cd /webapp && npm install && npm install -D tailwindcss@npm:@tailwindcss/postcss7-compat postcss@^7 autoprefixer@^9 && npm install @craco/craco

cd /webapp && yarn start