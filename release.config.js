let config = require('semantic-release-preconfigured-conventional-commits');
config.tagFormat = 'nanopub-router-${version}'
config.branches = ['release']
config.plugins.push(
  [
    "@semantic-release/exec",
    {
      "publishCmd": "docker buildx build --push --tag $IMAGE_NAME:${nextRelease.version} --tag $IMAGE_NAME:latest ."
    }
  ],
  "@semantic-release/github",
  "@semantic-release/git"
)
module.exports = config
