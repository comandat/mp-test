import sys
import time
from config import load_config
from browser_engine import create_browser

def test_nopecha():
    config = load_config()
    print("[*] Incercam sa lansam Camoufox cu extensia incarcata...")
    with create_browser(config) as page:
        print("[*] Browser pornit cu succes!")
        
        # Navigam catre demo-ul oficial de Recaptcha de la Nopecha
        print("[*] Vom naviga catre demo-ul de recaptcha...")
        page.goto("https://nopecha.com/demo/recaptcha")
        
        print("[*] Asteptam 15 secunde sa vedem daca extensia preia controlul captchei...")
        time.sleep(15)
        
        page.screenshot(path="nopecha_test.png")
        print("[*] Am salvat imaginea nopecha_test.png - verificam rezultatul!")

if __name__ == "__main__":
    test_nopecha()
