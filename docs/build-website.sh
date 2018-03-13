#!/bin/sh
# Keeping this file so we don't have to adjust Netlify settings
cd "$(dirname "$0")"
python3 build-website.py
