from dataclasses import dataclass
from pathlib import Path
from dotenv import load_dotenv
import os
import sys


@dataclass(frozen=True)
class Config:
    ddg_auth_token: str
    herosms_api_key: str
    herosms_service_code: str
    herosms_country_id: int
    herosms_base_url: str
    ddg_base_url: str
    emag_login_url: str
    extensions_dir: Path
    accounts_file: Path
    sms_poll_interval: int
    sms_poll_timeout: int
    max_phone_retries: int


# Known service codes from HeroSMS
KNOWN_SERVICES = {
    "emag": "bfg",
    "wolt": "rr",
    "macdonalds": "mb",
    "trendyol": "pr",
    "uber": "ub",
    "bolt": "tx",
    "shein": "aez",
    "glovo": "aq",
}

# Known country codes from HeroSMS
KNOWN_COUNTRIES = {
    "romania": 32,
    "polonia": 15,
    "uk": 16,
    "canada": 36,
}


def _require_env(key: str) -> str:
    value = os.getenv(key)
    if not value:
        sys.exit(f"FATAL: Missing required environment variable: {key}")
    return value


def load_config() -> Config:
    load_dotenv()

    project_root = Path(__file__).parent

    return Config(
        ddg_auth_token=_require_env("DDG_AUTH_TOKEN"),
        herosms_api_key=_require_env("HEROSMS_API_KEY"),
        herosms_service_code=os.getenv("HEROSMS_SERVICE_CODE", "bfg"),
        herosms_country_id=int(os.getenv("HEROSMS_COUNTRY_ID", "32")),
        herosms_base_url="https://hero-sms.com/stubs/handler_api.php",
        ddg_base_url="https://quack.duckduckgo.com/api/email/addresses",
        emag_login_url="https://www.emag.ro/user/login",
        extensions_dir=project_root / "extensions",
        accounts_file=project_root / "accounts.json",
        sms_poll_interval=5,
        sms_poll_timeout=180,
        max_phone_retries=5,
    )
