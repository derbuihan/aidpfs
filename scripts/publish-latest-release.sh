#!/usr/bin/env bash
set -euo pipefail

repo="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
commit="${GITHUB_SHA:?GITHUB_SHA is required}"
dist_dir="${AIDPFS_DIST_DIR:-dist}"
notes_file="$(mktemp)"
releases_file="$(mktemp)"
trap 'rm -f "$notes_file" "$releases_file"' EXIT

shopt -s nullglob
assets=("$dist_dir"/aidpfs_linux_*.tar.gz "$dist_dir"/aidpfs_*.deb "$dist_dir"/SHA256SUMS)
if ((${#assets[@]} != 5)); then
	echo "expected 2 Linux archives, 2 Debian packages, and SHA256SUMS in $dist_dir" >&2
	exit 1
fi

short_commit="${commit:0:7}"
cat >"$notes_file" <<EOF
Automated Linux preview built from commit $commit.

Supported targets: Linux amd64 and arm64; Debian/Ubuntu .deb packages and tar.gz archives.

See the included README for system requirements and installation details. SHA256SUMS verifies the attached files.
EOF

# Capture the full list before deleting anything. Abort if the API call fails or
# any published release is immutable, so no old release is removed in that case.
gh api --paginate "repos/$repo/releases?per_page=100" \
	--jq '.[] | [.tag_name, (.draft | tostring), (.immutable // false | tostring)] | @tsv' >"$releases_file"
mapfile -t releases <"$releases_file"
for release in "${releases[@]}"; do
	IFS=$'\t' read -r tag draft immutable <<<"$release"
	if [[ "$draft" == false && "$immutable" == true ]]; then
		echo "Cannot replace immutable release '$tag'; leaving all published releases untouched." >&2
		exit 1
	fi
done

for release in "${releases[@]}"; do
	IFS=$'\t' read -r tag draft _ <<<"$release"
	# Remove published releases and stale drafts for the moving latest tag.
	if [[ "$tag" == latest || "$draft" == false ]]; then
		gh release delete "$tag" --cleanup-tag --yes --repo "$repo"
	fi
done

gh release create latest "${assets[@]}" \
	--target "$commit" \
	--title "aidpfs latest preview ($short_commit)" \
	--notes-file "$notes_file" \
	--prerelease \
	--latest=false \
	--repo "$repo"

gh release view latest --repo "$repo" --json tagName,isDraft,isPrerelease,assets --jq '
  if .tagName == "latest" and .isDraft == false and .isPrerelease == true and
     ([.assets[].name] | index("SHA256SUMS")) != null and
     ([.assets[].name | select(endswith(".tar.gz"))] | length) == 2 and
     ([.assets[].name | select(endswith(".deb"))] | length) == 2 and
     ([.assets[]] | length) == 5
  then "latest preview has both Linux architectures and checksums"
  else error("published latest release is missing required metadata or assets") end'

tag_object="$(gh api "repos/$repo/git/ref/tags/latest" --jq '.object | [.type, .sha] | @tsv')"
IFS=$'\t' read -r object_type object_sha <<<"$tag_object"
if [[ "$object_type" == tag ]]; then
	object_sha="$(gh api "repos/$repo/git/tags/$object_sha" --jq '.object.sha')"
fi
if [[ "$object_sha" != "$commit" ]]; then
	echo "latest tag points to $object_sha instead of requested commit $commit" >&2
	exit 1
fi

release_count="$(gh release list --repo "$repo" --limit 1000 --json isDraft --jq '[.[] | select(.isDraft == false)] | length')"
if [[ "$release_count" != 1 ]]; then
	echo "expected exactly one published GitHub Release, found $release_count" >&2
	exit 1
fi

echo "Published the sole latest pre-release for $commit."
