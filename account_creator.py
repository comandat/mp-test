import logging
import random
import time
import re
import base64
from typing import Callable, Optional

from config import Config
from email_generator import generate_duck_email, derive_password
from sms_provider import (
    request_phone_number,
    poll_sms_code,
    complete_activation,
    cancel_activation,
    check_balance,
    SMSProviderError,
    SMSTimeout,
)
from browser_engine import create_browser
from storage import save_account

logger = logging.getLogger(__name__)

ROMANIAN_FIRST_NAMES = [
    "Andrei", "Alexandru", "Ion", "Mihai", "Cristian", "Stefan", "Daniel",
    "George", "Adrian", "Florin", "Maria", "Elena", "Ana", "Ioana",
    "Andreea", "Cristina", "Gabriela", "Raluca", "Diana", "Alina",
]

ROMANIAN_LAST_NAMES = [
    "Popescu", "Ionescu", "Popa", "Stan", "Dumitru", "Stoica", "Gheorghe",
    "Rusu", "Matei", "Constantin", "Munteanu", "Serban", "Moldovan",
    "Dinu", "Nistor", "Barbu", "Pavel", "Neagu", "Lazar", "Vlad",
]


def _generate_full_name() -> str:
    first = random.choice(ROMANIAN_FIRST_NAMES)
    last = random.choice(ROMANIAN_LAST_NAMES)
    return f"{first} {last}"


def _format_phone_for_emag(raw_phone: str) -> str:
    if raw_phone.startswith("40") and len(raw_phone) == 11:
        return "0" + raw_phone[2:]
    if raw_phone.startswith("+40"):
        return "0" + raw_phone[3:]
    return raw_phone


def _random_delay(min_sec: float = 0.5, max_sec: float = 1.5):
    time.sleep(random.uniform(min_sec, max_sec))


def _dismiss_cookie_banner(page):
    try:
        cookie_btn = page.locator("button:has-text('Accept'), [data-testid='cookie-accept']")
        if cookie_btn.count() > 0:
            cookie_btn.first.click()
            _random_delay(1, 2)
            logger.info("Cookie banner dismissed")
    except Exception:
        logger.debug("No cookie banner found or already dismissed")


def _step_enter_email(page, config: Config, email: str):
    logger.info(f"Step 1: Entering email {email}")
    page.goto(config.emag_login_url, wait_until="domcontentloaded")
    _random_delay(2, 3)

    _dismiss_cookie_banner(page)

    email_input = page.locator("input[type='email'], input[name*='email'], input[placeholder*='email' i]").first
    email_input.click()
    _random_delay()
    email_input.fill(email)
    _random_delay()

    page.locator("button:has-text('Continuă')").first.click()
    page.wait_for_load_state("domcontentloaded")
    _random_delay(2, 3)
    logger.info("Email submitted, waiting for registration form")


def _step_fill_registration(page, full_name: str, password: str):
    logger.info(f"Step 2: Filling registration form for {full_name}")

    page.wait_for_selector(
        "input[placeholder*='ume' i], input[name*='name' i]",
        timeout=15000,
    )
    _random_delay()

    name_input = page.locator("input[placeholder*='ume' i], input[name*='name' i]").first
    name_input.click()
    _random_delay()
    name_input.fill(full_name)
    _random_delay()

    password_inputs = page.locator("input[type='password']")
    password_inputs.nth(0).click()
    _random_delay()
    password_inputs.nth(0).fill(password)
    _random_delay()

    password_inputs.nth(1).click()
    _random_delay()
    password_inputs.nth(1).fill(password)
    _random_delay()

    terms_checkbox = page.locator(
        "input[type='checkbox']"
    ).first
    if not terms_checkbox.is_checked():
        terms_checkbox.click(force=True)
    _random_delay()

    page.locator("button:has-text('Continuă')").first.click()
    page.wait_for_load_state("domcontentloaded")
    _random_delay(2, 3)
    logger.info("Registration form submitted")


def _step_enter_phone(page, phone: str) -> bool:
    logger.info(f"Step 3: Entering phone number {phone}")

    try:
        page.wait_for_selector(
            "input[placeholder*='07' i], input[type='tel']",
            timeout=15000,
        )
    except Exception:
        logger.warning("Phone input not found, checking for errors")
        return False

    _random_delay()

    phone_input = page.locator("input[placeholder*='07' i], input[type='tel']").first
    phone_input.click()
    _random_delay()
    phone_input.fill(phone)
    _random_delay()

    page.locator("button:has-text('Trimite SMS')").first.click()
    page.wait_for_load_state("domcontentloaded")
    _random_delay(2, 3)

    error_visible = page.locator(".help-block, .is-invalid, .gui-field-error, .error-message").or_(
        page.locator("text=/acest număr|already|deja|deja asociat|invalid/i")
    )
    if error_visible.count() > 0:
        logger.warning(f"Phone {phone} rejected by eMAG: number already used or invalid")
        return False

    logger.info("Phone number submitted, waiting for SMS")
    return True


def _step_enter_sms_code(page, code: str):
    logger.info(f"Step 4: Entering SMS code {code}")

    page.wait_for_selector(
        "input[type='number'], #validate_mfa_code",
        timeout=15000,
    )
    _random_delay()

    digit_inputs = page.locator("input[type='number']")
    digit_count = digit_inputs.count()

    if digit_count >= 6:
        for i, digit in enumerate(code[:digit_count]):
            digit_inputs.nth(i).click()
            _random_delay(0.1, 0.3)
            digit_inputs.nth(i).fill(digit)
            _random_delay(0.1, 0.3)
    else:
        hidden_input = page.locator("#validate_mfa_code")
        hidden_input.evaluate(f"el => el.value = '{code}'")

    _random_delay()
    page.locator("#validate_mfa_continue, button:has-text('Continuă')").first.click()
    page.wait_for_load_state("domcontentloaded")
    _random_delay(2, 3)
    logger.info("SMS code submitted")


def _step_accept_genius(page):
    logger.info("Step 5: Accepting Genius and finishing up")
    # Asteptam ca ecranul sa se schimbe complet
    _random_delay(4, 6)
    
    genius_checkbox = page.locator(".g-checkbox, input.js-checkbox").first
    
    if genius_checkbox.count() > 0:
        logger.info("Genius prompt found, checking the box")
        genius_checkbox.click(force=True)
        _random_delay(1, 2)
        
        continue_btn = page.locator(".js-continue-btn").first
        if continue_btn.count() > 0:
            logger.info("Clicking continue with Genius")
            continue_btn.click()
            page.wait_for_load_state("domcontentloaded")
            _random_delay(2, 3)
    else:
        logger.info("No Genius prompt found, proceeding")


def _acquire_phone_and_verify(config: Config, page, emit: Callable) -> tuple[str, str, str]:
    for attempt in range(1, config.max_phone_retries + 1):
        logger.info(f"Phone attempt {attempt}/{config.max_phone_retries}")
        emit(f"Se incearca numarul de telefon (încercarea {attempt})...")

        activation_id, raw_phone = request_phone_number(config)
        formatted_phone = _format_phone_for_emag(raw_phone)

        phone_accepted = _step_enter_phone(page, formatted_phone)

        if not phone_accepted:
            msg = f"Telefon {formatted_phone} deja folosit, se caută altul..."
            logger.warning(msg)
            emit("Telefon deja folosit. Se anulează și se caută alt număr...")
            try:
                cancel_activation(config, activation_id)
            except Exception as e:
                logger.warning(f"Failed to cancel activation: {e}")
            
            _random_delay(2, 4)
            continue
            
        msg = f"Telefon {formatted_phone} acceptat. Se așteaptă SMS (max 5 min)..."
        logger.info(msg)
        emit(msg)
        try:
            sms_code = poll_sms_code(config, activation_id)
            # Remove complete_activation(id) here, so we keep it open for second SMS!
            return formatted_phone, sms_code, activation_id
        except SMSTimeout:
            logger.warning("Primul timeout s-a incheiat. Incercam sa apasam pe Retransmite SMS.")
            emit("Timpul inițial (5 min) a expirat fără mesaj. Apăsăm pe retrimitere SMS...")
            try:
                # Căutăm butonul de retransmitere și îl apăsăm
                retransmit_btn = page.locator("button:has-text('Retransmite SMS')")
                retransmit_btn.click(timeout=10000)
                
                emit("Am cerut retrimiterea de la eMAG. Așteptăm încă 5 minute...")
                logger.info("Polling for sms code again...")
                sms_code = poll_sms_code(config, activation_id)
                return formatted_phone, sms_code, activation_id
            except Exception as retry_e:
                logger.error(f"Eroare la a doua astepare/retransmitere: {retry_e}")
                emit("Eșec definitiv la recepționare cod.")
        except Exception as e:
            logger.error(f"SMS failed generally: {e}")

        # Daca am ajuns aici, a esuat ambele asteptari sau a apărut o excepție
        try:
            cancel_activation(config, activation_id)
        except Exception:
            pass
        
        logger.warning("Nu s-a putut obține codul. Se anulează. Solicităm o sesiune complet nouă.")
        emit("Nu s-a putut obține codul. Se anulează.")
        raise SMSCriticalFailureError("Zero SMS arrived after both timeouts.")


class SMSCriticalFailureError(Exception):
    pass

# Globals used for forceful interruption
_current_browser_ref = None

def force_stop():
    global _current_browser_ref
    if _current_browser_ref is not None:
        try:
            _current_browser_ref.close()
        except:
            pass
        _current_browser_ref = None

def _run_single_session(config: Config, emit: Callable) -> dict:
    global _current_browser_ref
    
    balance = check_balance(config)
    logger.info(f"HeroSMS balance: ${balance}")

    email = generate_duck_email(config)
    password = derive_password(email)
    full_name = _generate_full_name()

    logger.info(f"Email: {email}")
    logger.info(f"Password: {password}")
    logger.info(f"Name: {full_name}")

    emit(f"Pornire browser automat minimal...")
    with create_browser(config) as browser_and_page:
        _current_browser_ref = browser_and_page.context.browser

        try:
            emit("Se introduce email-ul...")
            _step_enter_email(browser_and_page, config, email)
            
            emit("Se completează formularul cu datele personale...")
            _step_fill_registration(browser_and_page, full_name, password)

            formatted_phone, sms_code, activation_id = _acquire_phone_and_verify(config, browser_and_page, emit)

            emit(f"S-a primit SMS! Introducem codul {sms_code}...")
            _step_enter_sms_code(browser_and_page, sms_code)
            
            emit("Se verifică bifa Genius...")
            _step_accept_genius(browser_and_page)

            account_data = {
                "email": email,
                "password": password,
                "phone": formatted_phone,
                "name": full_name,
                "activation_id": activation_id
            }

            save_account(config, email, password, formatted_phone)
            emit("Cont creat cu succes! Salvat pe server.")

            logger.info("=" * 60)
            logger.info("CONT CREAT CU SUCCES!")
            logger.info("=" * 60)

            return account_data

        except Exception as e:
            e_msg = str(e).lower()
            if "target closed" in e_msg or "context destroyed" in e_msg or "browser has been closed" in e_msg:
                raise RuntimeError("Proces Oprit Forțat")
            
            # Daca e SMSCriticalFailureError, o lăsăm să iasă curat ca să prindă retry-ul în create_account
            if isinstance(e, SMSCriticalFailureError):
                raise e

            try:
                emit("Eroare întâmpinată. Se capturează screenshot...")
                screenshot_bytes = browser_and_page.screenshot()
                import base64
                b64_image = base64.b64encode(screenshot_bytes).decode('utf-8')
                emit(f"SCREENSHOT:{b64_image}")
            except Exception as ss_e:
                logger.error(f"Failed to capture screenshot: {ss_e}")
            
            raise e
        finally:
            _current_browser_ref = None

def create_account(config: Config, status_callback: Optional[Callable[[str], None]] = None) -> dict:
    def emit(msg: str):
        if status_callback:
            status_callback(msg)
            
    for attempt in range(1, 3):  # Max 2 incercari complete (1 + 1 retry)
        try:
            current_config = config
            if attempt > 1:
                from dataclasses import replace
                emit(f"Începem din nou de la zero (Sesiunea {attempt}/2) folosind SMSPool API...")
                current_config = replace(config, 
                    herosms_base_url="https://api.smspool.net/stubs/handler_api", 
                    herosms_api_key="Ibl1Q0EpQ03BbfwzuTbE51McCgPlRxrw",
                    herosms_service_code="817",
                    herosms_country_id=13,
                    smspool_mode=True
                )
            return _run_single_session(current_config, emit)
        except SMSCriticalFailureError:
            if attempt == 1:
                emit("Eșec SMS prelungit. Închidem browser-ul complet și pornim o sesiune cu totul nouă (1 reîncercare rămasă)...")
                logger.warning("SMS critical failure. Restarting entire session once.")
                import time
                time.sleep(3)
                continue
            else:
                emit("A eșuat și a doua sesiune. Renunțăm.")
                raise RuntimeError("Eșec la primirea SMS-ului în multiple sesiuni noi.")
