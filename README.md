# Rinha de Backend 2025 - Submission by Jules

This repository contains the submission for the Rinha de Backend 2025 challenge, developed by Jules, an AI Software Engineer.

## Architecture

The solution is designed to be performant, resilient, and scalable, adhering to the challenge's requirements. It consists of the following components:

- **Load Balancer**: Nginx is used as a reverse proxy and load balancer, distributing incoming requests between two instances of the API server.
- **API Server**: Two instances of a custom-built API server written in Go. Go was chosen for its high performance, low memory footprint, and excellent concurrency support, making it ideal for this I/O-bound challenge.
- **Database**: A PostgreSQL database is used to store and manage payment transaction records, ensuring data consistency for the summary endpoint.

## Strategy

The core of the solution is a resilient payment processing strategy:

- **Health Checks**: Each API server instance runs a background process that periodically checks the health of the two payment processors (`default` and `fallback`).
- **Circuit Breaker**: A circuit breaker pattern is implemented for each processor. The breaker trips based on explicit health check failures or a series of consecutive failed payment attempts. This prevents the system from repeatedly calling an unhealthy service.
- **Intelligent Routing**: When a payment request is received, the API server checks the status of the circuit breakers. It prioritizes the `default` processor (due to its lower fee) and only uses the `fallback` processor if the default is unavailable. If both are unavailable, it returns a service unavailable error.

This combination of health checks and circuit breaking ensures high availability and maximizes profit by minimizing the use of the more expensive fallback processor.

## Technologies Used

- **Language**: Go
- **Database**: PostgreSQL
- **Load Balancer**: Nginx
- **Containerization**: Docker and Docker Compose

## Source Code

The source code for this solution can be found at: [https://github.com/user/rinha-de-backend-2025-jules](https://github.com/user/rinha-de-backend-2025-jules) (placeholder link).
