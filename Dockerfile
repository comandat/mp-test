FROM python:3.11-slim

# Instalarea pachetelor de sistem vitale pentru Firefox în mediu headless, altfel va da crash la lansare.
RUN apt-get update && apt-get install -y \
    wget \
    gnupg \
    libnss3 \
    libatk-bridge2.0-0 \
    libx11-xcb1 \
    libxcb-dri3-0 \
    libxtst6 \
    libgtk-3-0 \
    libxss1 \
    libasound2 \
    libgbm1 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copiem doar dependințele întâi pentru a eficientiza build cache-ul pe Railway
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Descărcăm browserul de la Camoufox plus datele de amprentă la BUILD TIME (viteza mai mare la runtime)
RUN python -m camoufox fetch

# Copiem restul codului aplicației în container
COPY . .

# Setăm portul default pentru Railway (dacă nu e specificat, setăm default 8080)
ENV PORT=8080
EXPOSE $PORT

# Rulăm direct serverul web
CMD ["python", "app.py"]
