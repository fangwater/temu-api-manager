module.exports = {
  apps: [
    {
      name: "temu-upload-console",
      cwd: "/home/ubuntu/temu-api-manager",
      script: "/home/ubuntu/.venv/bin/python",
      args: "-m temu_api_manager.web_server --host 127.0.0.1 --port 18082",
      env: {
        PYTHONPATH: "/home/ubuntu/temu-api-manager/src",
        PYTHONUNBUFFERED: "1",
      },
      autorestart: true,
      max_restarts: 10,
      restart_delay: 1000,
    },
  ],
};
