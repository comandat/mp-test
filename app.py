import os
import eventlet
eventlet.monkey_patch()

from flask import Flask, render_template
from flask_socketio import SocketIO, emit

app = Flask(__name__)
# Setăm cheia secreta daca e nevoie pe viitor (acum acceptăm tot)
app.config['SECRET_KEY'] = 'secret!'
socketio = SocketIO(app, cors_allowed_origins="*", async_mode='eventlet')

@app.route("/")
def index():
    return render_template("index.html")

# --- Comenzi de la Telefon (Client) spre Laptop (Worker) ---

@socketio.on("client_start")
def handle_client_start():
    print("▶ [HUB] Comandă de START preluată de pe telefon! O trimit spre laptop...")
    socketio.emit("worker_start")

@socketio.on("client_stop")
def handle_client_stop():
    print("🛑 [HUB] Comandă de OPRIT FORȚAT preluată de pe telefon! O trimit spre laptop...")
    socketio.emit("worker_stop")

@socketio.on("client_resend_sms")
def handle_client_resend(data):
    print("📩 [HUB] Comandă de RETRIMITE SMS preluată de pe telefon!")
    # trimitem catre worker ID-ul activarii ca argument
    socketio.emit("worker_resend_sms", data)


# --- Comenzi de la Laptop (Worker) spre Telefon (Client UI) ---

@socketio.on("worker_log")
def handle_worker_log(data):
    # data: {'msg': 'Se pornește browser..'} etc.
    # Il rostogolim spre telefon.
    socketio.emit("server_log", data)

if __name__ == "__main__":
    port = int(os.environ.get("PORT", 8080))
    print(f"☁ [HUB] Cloud Relay serverul ascultă pe portul {port}...")
    socketio.run(app, host="0.0.0.0", port=port)
