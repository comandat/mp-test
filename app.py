import queue
from flask import Flask, render_template, Response, request, jsonify
import sys
import os

# Add root dir to sys path for imports
sys.path.append(os.path.dirname(os.path.abspath(__file__)))

from account_creator import create_account
from sms_provider import resend_sms
from config import load_config
import logging

# Disable Flask default heavy logging to keep CLI clean
log = logging.getLogger('werkzeug')
log.setLevel(logging.ERROR)

app = Flask(__name__)

# Global queues to pipe status messages to SSE clients
client_queues = []

def notify_all(msg: str):
    for q in client_queues:
        try:
            q.put(msg)
        except Exception:
            pass

@app.route("/")
def index():
    return render_template("index.html")

@app.route("/api/status")
def status_stream():
    """Server-Sent Events endpoint to stream automation progress to the UI"""
    def event_stream():
        q = queue.Queue()
        client_queues.append(q)
        try:
            while True:
                msg = q.get()
                yield f"data: {msg}\n\n"
        except GeneratorExit:
            pass
        finally:
            if q in client_queues:
                client_queues.remove(q)
                
    return Response(event_stream(), mimetype="text/event-stream")

import threading
import json

@app.route("/api/generate", methods=["POST"])
def generate():
    """Endpoint that runs the account creation automation"""
    config = load_config()
    
    def status_callback(msg):
        notify_all(msg)

    def background_task():
        try:
            notify_all("Inițializare proces de generare cont...")
            account_data = create_account(config, status_callback=status_callback)
            
            # Trimite datele procesate spre interfață
            notify_all(f"DATA:{json.dumps(account_data)}")
            notify_all("FINISH")
        except Exception as e:
            error_msg = f"Eroare: {str(e)}"
            notify_all(error_msg)

    # Eliberează request-ul HTTP imediat și mută execuția în fundal
    t = threading.Thread(target=background_task)
    t.start()
    return jsonify({"status": "started"})

@app.route("/api/generate/stop", methods=["POST"])
def stop_generation():
    """Endpoint that forcefully aborts the running generation task"""
    from account_creator import force_stop
    try:
        force_stop()
        notify_all("Comandă Oprire Forțată primită!")
        return jsonify({"status": "stopped"})
    except Exception as e:
        return jsonify({"error": str(e)}), 500

@app.route("/api/sms/resend", methods=["POST"])
def resend():
    """Endpoint to trigger the second SMS via HeroSMS status=3"""
    data = request.get_json()
    activation_id = data.get("activation_id")
    
    if not activation_id:
        return jsonify({"error": "Missing activation_id"}), 400
        
    config = load_config()
    
    def background_task():
        try:
            from sms_provider import complete_activation
            notify_all("Așteptând al doilea SMS...")
            new_sms_code = resend_sms(config, activation_id)
            complete_activation(config, activation_id)
            notify_all(f"AL_DOILEA_SMS:{new_sms_code}")
        except Exception as e:
            error_msg = f"Eroare re-trimitere SMS: {str(e)}"
            notify_all(error_msg)
            
    t = threading.Thread(target=background_task)
    t.start()
    return jsonify({"status": "started"})

if __name__ == "__main__":
    app.run(host="0.0.0.0", port=int(os.environ.get("PORT", 8080)), threaded=True)
