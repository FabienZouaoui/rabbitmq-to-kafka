package main

import (
	"github.com/Shopify/sarama"
	"github.com/streadway/amqp"

	"fmt"
	"log"
	"flag"
	"strings"
	"time"
	"strconv"

	//"encoding/json"
	"os"
)

const(
	DEFAULT_RMQ_USER     = "guest"
	DEFAULT_RMQ_PASS     = "guest"
	DEFAULT_RMQ_HOST     = "rabbitmq-front"
	DEFAULT_RMQ_PORT     = 5672
	DEFAULT_RMQ_QUEUE    = "KAFKA_QUEUE"
	DEFAULT_RMQ_EXCHANGE = "REQUESTS_EXCHANGE"

	DEFAULT_KAFKA_BROKERS   = "kafka:9092"
	DEFAULT_KAFKA_TOPIC     = "requests"
	DEFAULT_KAFKA_CLIENT_ID = "rabbitmq-to-kafka"
)

var (
	verbose = flag.Bool("verbose", false, "Turn on verbose logging")

	rabbitMQUser  = flag.String("rabbitmq-user", DEFAULT_RMQ_USER, "RabbitMQ username")
	rabbitMQPass  = flag.String("rabbitmq-pass", DEFAULT_RMQ_PASS, "RabbitMQ password")
	rabbitMQHost  = flag.String("rabbitmq-host", DEFAULT_RMQ_HOST, "Rabbitmq host to bind to")
	rabbitMQPort  = flag.Int("rabbitmq-port", DEFAULT_RMQ_PORT, "Rabbitmq port to bind to")
	rabbitMQQueue = flag.String("rabbitmq-queue", DEFAULT_RMQ_QUEUE, "Rabbitmq queue to consume")
	rabbitMQExchg = flag.String("rabbitmq-exchange", DEFAULT_RMQ_EXCHANGE, "RabbitMQ exchange to bind to")

	kafkaBrokers  = flag.String("kafka-brokers", DEFAULT_KAFKA_BROKERS, "Kafka brokers in host:port format, as a comma separated list")
	kafkaTopic    = flag.String("kafka-topic", DEFAULT_KAFKA_TOPIC, "Kafka topic on which to publish messages")
	kafkaClientID = flag.String("kafka-client-id", DEFAULT_KAFKA_CLIENT_ID, "Kafka client ID")

	certFile  = flag.String("certificate", "", "The optional certificate file for client authentication")
	keyFile   = flag.String("key", "", "The optional key file for client authentication")
	caFile    = flag.String("ca", "", "The optional certificate authority file for TLS client authentication")
	verifySsl = flag.Bool("verify", false, "Optional verify ssl certificates chain")

	addr      = flag.String("addr", ":8080", "The address to bind to")

	rabbitCloseError chan *amqp.Error
)

func rabbitSetup(url, exchange, queue string) (*amqp.Connection, *amqp.Channel, error) {
	rabbitCloseError = make(chan *amqp.Error)

	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, nil, err
	}
	conn.NotifyClose(rabbitCloseError)

	ch, err := conn.Channel()
	if err != nil {
		return nil, nil, err
	}

	q, err := ch.QueueDeclare(
		queue,
		true,  // durable
		false, // delete when unused
		false, // exclusive
		false, // no-wait
		map[string]interface{}{"x-queue-mode": "lazy"},
	)
	if err != nil {
		return nil, nil, err
	}

	if err := ch.QueueBind(
		q.Name,
		"", // routing key
		exchange,
		false, // no-wait
		nil,   // args
	); err != nil {
		return nil, nil, err
	}

	return conn, ch, nil
}

func rabbitConsume(url, exchange, queue string) (*amqp.Connection, <-chan amqp.Delivery, error) {
	conn, ch, err := rabbitSetup(url, exchange, queue)
	if err != nil {
		return nil, nil, err
	}

	msgs, err := ch.Consume(
		queue,
		"sirdata-monitoring", // consumer
		true,                 // auto-ack
		false,                // exclusive
		false,                // no-local
		false,                // no-wait
		nil,                  // args
	)
	return conn, msgs, err
}

func rabbitmConnError() {
	var rabbitErr *amqp.Error
	for {
		rabbitErr = <-rabbitCloseError
		if rabbitErr != nil {
			log.Fatalf("%s: %s", "Rabbitmq connexion error", rabbitErr)
		}
	}
}

func main() {
	flag.Parse()

	if *verbose {
		sarama.Logger = log.New(os.Stdout, "[sarama] ", log.LstdFlags)
	}

	// Kafka
	brokerList := strings.Split(*kafkaBrokers, ",")
	log.Printf("Kafka brokers: %s", strings.Join(brokerList, ", "))

	/* https://github.com/Shopify/sarama/issues/805 and https://github.com/Shopify/sarama/issues/959 */
	sarama.MaxRequestSize = 1000000

	config := sarama.NewConfig()
	config.ClientID = *kafkaClientID
	config.Producer.RequiredAcks = sarama.WaitForAll
	config.Producer.Retry.Max = 5
	config.Producer.Return.Successes = false // If set to true, I must read AsyncProducer.Successes() channel or it will deadlock


	producer, err := sarama.NewAsyncProducer(brokerList, config)
	if err != nil {
		panic(err)
	}
	defer func() {
		if err := producer.Close(); err != nil {
			panic(err)
		}
	}()

	// RabbitMQ
	rabbitUrl := fmt.Sprintf("amqp://%s:%s@%s:%d/", *rabbitMQUser, *rabbitMQPass, *rabbitMQHost, *rabbitMQPort)
	_, msgs, err := rabbitConsume(rabbitUrl, *rabbitMQExchg, *rabbitMQQueue)
	if err != nil {
		log.Fatalf("%s: %s", "Failed to register RabbitmMQ consumer", err)
	}
	go rabbitmConnError()

	for b := range msgs {
		if *verbose {
			log.Printf("Received a message: %s", b.Body)
		}

		strTime := strconv.Itoa(int(time.Now().Unix()))
		msg := &sarama.ProducerMessage{
			Topic: *kafkaTopic,
			Key: sarama.StringEncoder(strTime),
			Value: sarama.ByteEncoder(b.Body),
		}

		select {
		case producer.Input () <- msg:
			if *verbose {
				log.Printf("Message produced")
			}
		case err := <-producer.Errors():
			log.Fatalf("%s: %s", "Failed to produce message", err)
		}
	}
}
