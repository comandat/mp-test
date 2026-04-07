import httpx
import logging
from config import Config

logger = logging.getLogger(__name__)

DDG_HEADERS = {
    "sec-ch-ua-platform": '"Windows"',
    "Referer": "https://duckduckgo.com/",
    "User-Agent": (
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "
        "AppleWebKit/537.36 (KHTML, like Gecko) "
        "Chrome/146.0.0.0 Safari/537.36"
    ),
    "sec-ch-ua": '"Chromium";v="146", "Not-A.Brand";v="24", "Google Chrome";v="146"',
    "sec-ch-ua-mobile": "?0",
}


def generate_duck_email(config: Config) -> str:
    headers = {**DDG_HEADERS, "Authorization": f"Bearer {config.ddg_auth_token}"}

    response = httpx.post(config.ddg_base_url, headers=headers, timeout=30)
    response.raise_for_status()

    data = response.json()
    address = data.get("address", "")

    if not address:
        raise ValueError(f"DuckDuckGo returned empty address: {data}")

    email = f"{address}@duck.com"
    logger.info(f"Generated email: {email}")
    return email


def derive_password(email: str) -> str:
    return email[:-1] + email[-1].upper() + "1"
