#!/usr/bin/env python3
"""Set a GitHub commit status that links to a URL (shown in the PR checks list).

Usage: commit_status.py <repo> <sha> <token-file> <context> <target-url> <description>

Skips (exit 0) when the token file is missing or sha is empty.
"""
import json
import os
import sys
import urllib.request

API = "https://api.github.com"


def main() -> int:
    repo, sha, token_file, context, url, description = sys.argv[1:7]
    if not sha:
        print("No commit sha, skipping commit status")
        return 0
    if not os.path.isfile(token_file):
        print(f"{token_file} not found, skipping commit status")
        return 0

    with open(token_file) as f:
        token = f.read().strip()
    req = urllib.request.Request(
        f"{API}/repos/{repo}/statuses/{sha}",
        method="POST",
        data=json.dumps(
            {
                "state": "success",
                "context": context,
                "target_url": url,
                "description": description,
            }
        ).encode(),
        headers={
            "Authorization": f"Bearer {token}",
            "Accept": "application/vnd.github+json",
            "X-GitHub-Api-Version": "2022-11-28",
        },
    )
    urllib.request.urlopen(req, timeout=30).close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
