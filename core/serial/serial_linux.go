//go:build linux

package serial

import (
	"golang.org/x/sys/unix"
)

func configurePort(fd int, cfg Config) error {
	termios, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return err
	}

	// 原始模式 (Raw mode)，禁用特殊处理
	termios.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	termios.Oflag &^= unix.OPOST
	termios.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN

	// 启用接收器并忽略调制解调器控制线
	termios.Cflag |= unix.CREAD | unix.CLOCAL

	// 数据位
	termios.Cflag &^= unix.CSIZE
	switch cfg.DataBits {
	case 5:
		termios.Cflag |= unix.CS5
	case 6:
		termios.Cflag |= unix.CS6
	case 7:
		termios.Cflag |= unix.CS7
	case 8:
		termios.Cflag |= unix.CS8
	default:
		return ErrInvalidConfig
	}

	// 停止位
	switch cfg.StopBits {
	case StopBits1:
		termios.Cflag &^= unix.CSTOPB
	case StopBits2:
		termios.Cflag |= unix.CSTOPB
	default:
		return ErrInvalidConfig
	}

	// 奇偶校验
	switch cfg.Parity {
	case ParityNone:
		termios.Cflag &^= unix.PARENB
	case ParityOdd:
		termios.Cflag |= unix.PARENB | unix.PARODD
	case ParityEven:
		termios.Cflag |= unix.PARENB
		termios.Cflag &^= unix.PARODD
	default:
		termios.Cflag &^= unix.PARENB
	}

	// 波特率
	baud, ok := baudRatesLinux[cfg.BaudRate]
	if !ok {
		return ErrUnsupportedBaudRate
	}
	termios.Cflag &^= unix.CBAUD
	termios.Cflag |= baud

	termios.Cc[unix.VMIN] = 1
	termios.Cc[unix.VTIME] = 0

	return unix.IoctlSetTermios(fd, unix.TCSETS, termios)
}

var baudRatesLinux = map[int]uint32{
	50:     unix.B50,
	75:     unix.B75,
	110:    unix.B110,
	134:    unix.B134,
	150:    unix.B150,
	200:    unix.B200,
	300:    unix.B300,
	600:    unix.B600,
	1200:   unix.B1200,
	1800:   unix.B1800,
	2400:   unix.B2400,
	4800:   unix.B4800,
	9600:   unix.B9600,
	19200:  unix.B19200,
	38400:  unix.B38400,
	57600:  unix.B57600,
	115200: unix.B115200,
	230400: unix.B230400,
	460800: unix.B460800,
	921600: unix.B921600,
}
