import logging
import os
from typing import Any
from urllib.parse import parse_qs, urljoin, urlparse

import requests
from requests import Response

logger = logging.getLogger(__name__)

GH_USERNAME = os.getenv("GH_USERNAME")
GH_PAT = os.getenv("GH_PAT")
THRESHOLD = int(os.getenv("ANTIBOT_THRESHOLD", 20_000))
WHITELIST = {u.strip() for u in os.getenv("ANTIBOT_WHITELIST", "").split(",") if u}


class Github:
    BASE_URL = "https://api.github.com"

    def __init__(self) -> None:
        if not GH_PAT:
            raise RuntimeError("GH_PAT is required")
        self.headers = {
            "Authorization": f"Bearer {GH_PAT}",
            "Accept": "application/vnd.github+json",
            "X-GitHub-Api-Version": "2022-11-28",
            "User-Agent": GH_USERNAME or "anti-bot",
        }

    def _request(self, method: str, endpoint: str, **kwargs) -> Response:
        url = urljoin(self.BASE_URL, endpoint)
        response = requests.request(method, url, headers=self.headers, **kwargs)
        response.raise_for_status()
        return response

    def get_followers(self) -> list[dict[str, Any]]:
        followers = []
        url = f"/users/{GH_USERNAME}/followers?per_page=100"
        while url:
            resp = self._request("GET", url)
            followers.extend(resp.json())
            url = resp.links.get("next", {}).get("url")
        return followers

    def get_following_count(self, username: str) -> int:
        resp = self._request("HEAD", f"/users/{username}/following", params={"per_page": 1})
        if "last" in resp.links:
            last_url = resp.links["last"]["url"]
            return int(parse_qs(urlparse(last_url).query)["page"][0])
        logger.warning("Could not determine following count for %s", username)
        return 0

    def block_user(self, username: str) -> None:
        self._request("PUT", f"/user/blocks/{username}")
        logger.info("Blocked user: %s", username)


def main() -> None:
    github = Github()
    blocked = 0

    for follower in github.get_followers():
        username = follower["login"]
        if username in WHITELIST:
            continue
        following_count = github.get_following_count(username)
        if following_count >= THRESHOLD:
            logger.warning("Blocking user %s (follows %d users)", username, following_count)
            github.block_user(username)
            blocked += 1

    logger.info("Finished. Blocked %d users.", blocked)


if __name__ == "__main__":
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s - %(levelname)s - %(message)s",
        datefmt="%Y-%m-%d %H:%M:%S"
    )
    main()
