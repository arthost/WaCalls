#!/bin/sh
# Shell script to build the custom production VoIP calls image.
# Usage: ./make-image.sh [GITHUB_TOKEN] [GIT_BRANCH]
# Example: ./make-image.sh your_github_pat_token feature/wacalls-app

TOKEN=${1:-""}
BRANCH=${2:-"feature/wacalls-app"}

echo "Building duocrm-calls:custom from GitHub branch '${BRANCH}'..."

if [ -n "$TOKEN" ]; then
  docker build . -f Dockerfile.custom -t duocrm-calls:custom --build-arg GITHUB_TOKEN="$TOKEN" --build-arg GIT_BRANCH="$BRANCH" --no-cache
else
  docker build . -f Dockerfile.custom -t duocrm-calls:custom --build-arg GIT_BRANCH="$BRANCH" --no-cache
fi
