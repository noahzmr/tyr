case $- in *i*)
	echo "Tyr live shell. The acceptance test runs on tty1 (Alt+F1), the live view on tty3 (Alt+F3)."
	echo "Results: /mnt/results/tyr (TYR-RESULTS stick) or /var/lib/tyr/accept (memory)."
	echo "Avoid SMART queries (smartctl, tyr without --no-smart) while the test runs: on some"
	echo "controllers they count as disk errors."
	;;
esac
