#!/usr/bin/env bash
# Prints credentials for the installer to display after successful startup.
spoof_setup_credentials() {
  local binary="$1" data="$2" action="${SPOOF_PASSWORD_ACTION:-ask}" choice new_password
  case "$action" in ask|keep|reset) ;; *) echo 'SPOOF_PASSWORD_ACTION must be ask, keep or reset.' >&2; return 1 ;; esac
  if [[ "$action" == ask ]]; then
    action=keep
    if [[ -f "$data/credentials.txt" ]] && { exec 3<>/dev/tty; } 2>/dev/null; then
      printf '\nPanel password / رمز ورود پنل:\n  1) Keep current password / حفظ رمز فعلی (default)\n  2) Generate and show a new password / تولید و نمایش رمز جدید\nChoose [1/2]: ' >&3
      IFS= read -r choice <&3 || choice=1
      exec 3>&-
      case "$choice" in 2) action=reset ;; 1|'') ;; *) echo 'Invalid choice; keeping the current password.' >&2 ;; esac
    fi
  fi
  if [[ "$action" == reset ]]; then
    new_password=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
    [[ ${#new_password} == 32 ]] || { echo 'Password generation failed.' >&2; return 1; }
    SPOOF_DATA_DIR="$data" "$binary" --setup --setup-pass "$new_password"
    unset new_password
  else
    SPOOF_DATA_DIR="$data" "$binary" --setup
  fi
}
