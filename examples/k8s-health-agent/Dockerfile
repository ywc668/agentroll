FROM python:3.12-slim

WORKDIR /app

# Install dependencies
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy agent code
COPY agent.py .

# Expose port
EXPOSE 8080

# Run the agent
CMD ["uvicorn", "agent:app", "--host", "0.0.0.0", "--port", "8080"]
