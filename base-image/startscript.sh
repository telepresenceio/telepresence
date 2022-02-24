#! /bin/sh
USER_ID=$(id -u)
if [ "$2" = "agent" ] && [ "$USER_ID" -ne "0" ]; then
  export USER_ID
  export GROUP_ID=$(id -g)
  echo "Initializing nss_wrapper. USER_NAME = $USER_NAME, UID = $USER_ID, GID = $GROUP_ID"
  cp /passwd.template ${NSS_WRAPPER_PASSWD}
  echo "${USER_NAME}:x:${USER_ID}:${GROUP_ID}:${USER_NAME}:/home/${USER_NAME}:/bin/sh" >> ${NSS_WRAPPER_PASSWD}
  export LD_PRELOAD=libnss_wrapper.so
fi
exec $@
