import logging
import sys

from config import load_config
from account_creator import create_account


def setup_logging():
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
        datefmt="%H:%M:%S",
        handlers=[
            logging.StreamHandler(sys.stdout),
            logging.FileHandler("emag_generator.log", encoding="utf-8"),
        ],
    )


def main():
    setup_logging()
    logger = logging.getLogger(__name__)

    logger.info("=== eMAG Account Generator ===")

    config = load_config()
    logger.info("Configuration loaded successfully")

    try:
        account = create_account(config)
        logger.info(f"Account created: {account['email']}")
    except KeyboardInterrupt:
        logger.warning("Interrupted by user")
        sys.exit(1)
    except Exception as e:
        logger.error(f"Account creation failed: {e}", exc_info=True)
        sys.exit(1)


if __name__ == "__main__":
    main()
