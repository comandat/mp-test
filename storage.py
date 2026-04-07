import json
import logging
from datetime import datetime, timezone
from pathlib import Path
from config import Config

logger = logging.getLogger(__name__)


def _read_accounts(path: Path) -> list[dict]:
    if not path.exists():
        return []
    return json.loads(path.read_text(encoding="utf-8"))


def save_account(config: Config, email: str, password: str, phone: str) -> None:
    accounts = _read_accounts(config.accounts_file)

    account = {
        "email": email,
        "password": password,
        "phone": phone,
        "created_at": datetime.now(timezone.utc).isoformat(),
    }

    accounts.append(account)
    config.accounts_file.write_text(
        json.dumps(accounts, indent=2, ensure_ascii=False),
        encoding="utf-8",
    )

    logger.info(f"Account saved: {email}")


def load_accounts(config: Config) -> list[dict]:
    return _read_accounts(config.accounts_file)
