import httpx
import time
import logging
from config import Config

logger = logging.getLogger(__name__)


class SMSProviderError(Exception):
    pass


class NoNumbersAvailable(SMSProviderError):
    pass


class PhoneAlreadyUsed(SMSProviderError):
    pass


class SMSTimeout(SMSProviderError):
    pass


def _herosms_request(config: Config, params: dict) -> str:
    all_params = {"api_key": config.herosms_api_key, **params}
    if getattr(config, "smspool_mode", False):
        all_params["setting"] = "smspool"
    response = httpx.get(config.herosms_base_url, params=all_params, timeout=30)
    response.raise_for_status()
    return response.text.strip()


def check_balance(config: Config) -> float:
    result = _herosms_request(config, {"action": "getBalance"})

    if not result.startswith("ACCESS_BALANCE:"):
        raise SMSProviderError(f"Unexpected balance response: {result}")

    balance = float(result.split(":")[1])
    logger.info(f"HeroSMS balance: ${balance}")
    return balance


def request_phone_number(config: Config) -> tuple[str, str]:
    result = _herosms_request(config, {
        "action": "getNumber",
        "service": config.herosms_service_code,
        "country": config.herosms_country_id,
    })

    if result == "NO_NUMBERS":
        raise NoNumbersAvailable("No phone numbers available for this service/country")

    if not result.startswith("ACCESS_NUMBER:"):
        raise SMSProviderError(f"Unexpected getNumber response: {result}")

    parts = result.split(":")
    activation_id = parts[1]
    phone_number = parts[2]

    logger.info(f"Got phone number: {phone_number} (activation: {activation_id})")
    return activation_id, phone_number


def poll_sms_code(config: Config, activation_id: str) -> str:
    elapsed = 0

    while elapsed < config.sms_poll_timeout:
        result = _herosms_request(config, {
            "action": "getStatus",
            "id": activation_id,
        })

        if result.startswith("STATUS_OK:"):
            code = result.split(":")[1]
            logger.info(f"SMS code received: {code}")
            return code

        if result == "STATUS_CANCEL":
            raise SMSProviderError(f"Activation {activation_id} was cancelled")

        logger.debug(f"Waiting for SMS... ({result})")
        time.sleep(config.sms_poll_interval)
        elapsed += config.sms_poll_interval

    raise SMSTimeout(f"SMS code not received within {config.sms_poll_timeout}s")


def complete_activation(config: Config, activation_id: str) -> None:
    result = _herosms_request(config, {
        "action": "setStatus",
        "id": activation_id,
        "status": 6,
    })
    logger.info(f"Activation {activation_id} completed: {result}")


def cancel_activation(config: Config, activation_id: str) -> None:
    """Cancels the activation (status 8) and refunds balance."""
    result = _herosms_request(config, {
        "action": "setStatus",
        "id": activation_id,
        "status": 8,
    })
    
    # We might get ACCESS_CANCEL or other responses
    logger.info(f"Activation {activation_id} cancelled: {result}")


def resend_sms(config: Config, activation_id: str) -> str:
    """
    Requests another SMS for the same activation_id (status 3).
    Returns the new SMS code via polling.
    """
    logger.info(f"Requesting second SMS for activation {activation_id} (status=3)...")
    result = _herosms_request(config, {
        "action": "setStatus",
        "id": activation_id,
        "status": 3,
    })
    
    if result != "ACCESS_RETRY_GET":
        logger.warning(f"Unexpected response for status=3: {result}")
        # Sometimes providers just accept it implicitly, we will proceed to poll anyway

    return poll_sms_code(config, activation_id)
