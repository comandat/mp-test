import zipfile
import logging
from pathlib import Path
from contextlib import contextmanager

import httpx
from camoufox.sync_api import Camoufox
from config import Config

logger = logging.getLogger(__name__)

NOPECHA_RELEASE_URL = (
    "https://github.com/NopeCHALLC/nopecha-extension/releases/latest/download/"
    "firefox_automation.zip"
)


def _ensure_nopecha_extension(config: Config) -> Path:
    nopecha_dir = config.extensions_dir / "nopecha"

    if nopecha_dir.exists() and (nopecha_dir / "manifest.json").exists():
        logger.info("NopeCHA extension already present")
        return nopecha_dir

    config.extensions_dir.mkdir(parents=True, exist_ok=True)
    zip_path = config.extensions_dir / "firefox_automation.zip"

    logger.info("Downloading NopeCHA firefox automation build...")
    response = httpx.get(NOPECHA_RELEASE_URL, follow_redirects=True, timeout=60)
    response.raise_for_status()
    zip_path.write_bytes(response.content)

    logger.info("Extracting NopeCHA extension...")
    nopecha_dir.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(zip_path, "r") as zf:
        zf.extractall(nopecha_dir)
    zip_path.unlink()

    logger.info(f"NopeCHA extension ready at {nopecha_dir}")
    return nopecha_dir


@contextmanager
def create_browser(config: Config):
    nopecha_path = _ensure_nopecha_extension(config)

    logger.info("Launching Camoufox browser (Headless)...")
    # For Railway or Linux headless usage, Headless must be True
    with Camoufox(
        headless=True,
        humanize=True,
        addons=[str(nopecha_path)],
    ) as browser:
        page = browser.new_page()
        yield page
