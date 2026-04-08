import socketio
import threading
import os
import time
from dotenv import load_dotenv

from account_creator import create_account, force_stop
from config import load_config
from sms_provider import complete_activation, resend_sms

# Creăm un client standard SocketIO pentru a acționa ca un sclav (Worker) la Hub-ul din Cloud
sio = socketio.Client()

# Încărcăm configurația
try:
    config_obj = load_config()
except Exception as e:
    print(f"[FATAL] Eroare citire config local: {e}")
    exit(1)

def emit_status(msg: str):
    """Prinde toate jurnalele log și le împinge asincron înapoi pe țeavă spre Cloud"""
    try:
        sio.emit("worker_log", {"msg": msg})
    except Exception as e:
        print(f"[Avertisment] Nu s-a putut trimite log spre root: {e}")

@sio.event
def connect():
    print("✅ [WORKER] Conexiunea la HUB s-a stabilit cu real-time succes!")

@sio.event
def disconnect():
    print("❌ [WORKER] M-am deconectat de la HUB.")

@sio.on("worker_start")
def worker_start():
    print("▶ [WORKER] Ordin de inițiere START recepționat din Cloud!")
    
    def run_task():
        try:
            emit_status("Conexiune directă cu PC-ul tău stabilită! Lansez mediul virtual Camoufox...")
            import json
            
            # create_account trimite prin callback direct logurile "normale" și erorile SCREENSHOT:
            account_data = create_account(config_obj, status_callback=emit_status)
            
            # La succes, livram obiectul de date inapoi clientului pe format string JSON
            emit_status(f"DATA:{json.dumps(account_data)}")
            emit_status("FINISH")
            print("[WORKER] Generator completat și transmis în eter.")
            
        except Exception as e:
            emit_status(f"Eroare: {str(e)}")
            
    # Izolăm operațiunea masivă într-un thread nou pentru a nu bloca viața socket-ului de comunicație
    t = threading.Thread(target=run_task)
    t.start()

@sio.on("worker_stop")
def worker_stop():
    print("🛑 [WORKER] Ordin FORCE STOP primit de pe front-end!")
    try:
        force_stop()
        emit_status("Proces oprit forțat de pe dispozitiv!")
    except Exception as e:
        print(f"[WORKER] Eroare internă la force stop: {e}")

@sio.on("worker_resend_sms")
def worker_resend_sms(data):
    print("📩 [WORKER] Ordin primit: Așteaptă al doilea cod SMS!")
    activation_id = data.get("activation_id")
    
    def run_resend():
        try:
            emit_status("Se așteaptă al doilea SMS pe backend...")
            new_sms_code = resend_sms(config_obj, activation_id)
            complete_activation(config_obj, activation_id)
            emit_status(f"AL_DOILEA_SMS:{new_sms_code}")
        except Exception as e:
            emit_status(f"Eroare re-trimitere SMS: {str(e)}")
            
    t = threading.Thread(target=run_resend)
    t.start()


if __name__ == "__main__":
    load_dotenv()
    # Adresa HUB_URL va fi localhost:8080 momentan, dar cand pui aplicatia frontend pe Railway
    # poti schimba intr-un .env cu HUB_URL="https://nume-app.up.railway.app"
    hub_url = os.environ.get("HUB_URL", "http://localhost:8080")
    
    print("=" * 60)
    print(f"⚡ WORKER LOCAL PENTRU EMAG GENERATOR ⚡")
    print(f"Încep căutarea radar spre Hub-ul Cloud configurat la: {hub_url}")
    print("Lasă fereastra asta deschisă cât timp folosești telefonul!\n")
    print("=" * 60)
    
    while True:
        try:
            sio.connect(hub_url)
            sio.wait()
        except Exception as e:
            print(f"Eroare conexiune la net {hub_url}.. reincep auto linkul in 5 secunde.")
            time.sleep(5)
