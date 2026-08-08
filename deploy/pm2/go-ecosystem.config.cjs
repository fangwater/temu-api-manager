module.exports = {
  apps: [
    {
      name: "temu-fulfillment-console",
      cwd: "/home/ubuntu/temu-api-manager",
      script: "./scripts/run-go-server.sh",
      interpreter: "none",
      autorestart: true,
      max_restarts: 10,
      restart_delay: 2000,
      merge_logs: true,
    },
  ],
};
