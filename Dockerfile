# Use official Python 3.11 Alpine image
FROM python:3.11-alpine

# Install build dependencies needed for some packages
RUN apk add --no-cache gcc musl-dev libffi-dev openssl-dev

WORKDIR /app

# Copy requirements and install dependencies
COPY requirements.txt .

RUN pip install --no-cache-dir -r requirements.txt

# Copy application code only
COPY main.py .

# Default command to run your bot
CMD ["python3", "main.py"]
